package zip

import (
	"context"
	"reflect"
	"strconv"
	"strings"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// TypedHandler is the generic handler signature: takes an *In, returns
// (*Out, error). zip generates OpenAPI 3.1 spec from the In/Out types
// and registers a Fiber route that unmarshals body → In, runs the
// handler, and marshals Out → JSON response.
type TypedHandler[In, Out any] func(ctx context.Context, in *In) (*Out, error)

// registeredOp is the bookkeeping zip keeps for a typed route — the ONE value
// every projection reads: the REST route, the OpenAPI doc, and the MCP tool all
// come from this. invoke is the transport-agnostic handler core (decode → run →
// result), so a REST request and an MCP tools/call run the exact same fn.
type registeredOp struct {
	Method      string
	Path        string
	OperationID string
	Summary     string
	Tags        []string
	InType      reflect.Type
	OutType     reflect.Type
	invoke      func(ctx context.Context, rawIn []byte, query, path map[string]string) (any, error)
}

// Get registers a GET typed handler at path.
func Get[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "GET", path, fn, opts...)
}

// Post registers a POST typed handler at path.
func Post[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "POST", path, fn, opts...)
}

// Put registers a PUT typed handler at path.
func Put[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "PUT", path, fn, opts...)
}

// Patch registers a PATCH typed handler at path.
func Patch[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "PATCH", path, fn, opts...)
}

// Delete registers a DELETE typed handler at path.
func Delete[In, Out any](app *App, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(app, "DELETE", path, fn, opts...)
}

// OpOption configures a typed handler registration (OpenAPI metadata).
type OpOption func(*registeredOp)

// WithSummary sets the operation summary in OpenAPI.
func WithSummary(s string) OpOption { return func(op *registeredOp) { op.Summary = s } }

// WithTags sets the operation tags in OpenAPI.
func WithTags(tags ...string) OpOption { return func(op *registeredOp) { op.Tags = tags } }

// WithOperationID sets the operation ID in OpenAPI.
func WithOperationID(id string) OpOption {
	return func(op *registeredOp) { op.OperationID = id }
}

// bindURL copies URL-borne values onto the decoded input, matching a name to the
// field whose json tag (else field name, case-insensitively) equals it. It is the
// ONE binder for both URL sources — path params and query params are the same
// kind of value (a name and a string, carried by the URL), so they get the same
// function rather than two that drift.
//
// Values arrive as strings on the wire and are converted to the field's kind:
// string, bool, the sized ints/uints, and the floats. An unparseable value leaves
// the field at its zero value rather than failing the request — `?limit=abc` is a
// caller's typo about ONE field, and refusing the whole call would make every
// typed GET brittler than the untyped handler it replaced. Declare `validate:` on
// the field to make a value mandatory.
//
// A name with no matching field is silently ignored: the route pattern and the
// input type are written together, so a mismatch is a programming error the
// OpenAPI projection surfaces, not a request to reject — refusing here would turn
// a spec typo into a 400 on every call to that route. Unknown query keys are
// ordinary (callers append tracking params), so ignoring them is required, not
// merely tolerant.
//
// Only the top level is walked. A nested field is not a URL target: the URL
// addresses one resource, and an input that nests its record declares its target
// explicitly (see the authorizer's `owned` interface) rather than having it
// guessed out of a sub-struct an attacker also controls.
func bindURL(in any, values map[string]string) {
	if len(values) == 0 {
		return
	}
	v := reflect.ValueOf(in)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		if !fv.CanSet() {
			continue
		}
		name := jsonFieldName(f)
		for k, val := range values {
			if strings.EqualFold(k, name) {
				setScalar(fv, val)
				break
			}
		}
	}
}

// setScalar writes one wire string into one field, converting by the field's
// kind. Anything it cannot represent (structs, slices, maps, pointers) is left
// alone — a URL carries scalars.
func setScalar(fv reflect.Value, val string) {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(val)
	case reflect.Bool:
		// An empty value means "flag present" — `?debug` reads as true, the
		// convention every HTML form and CLI already uses.
		if val == "" {
			fv.SetBool(true)
			return
		}
		if b, err := strconv.ParseBool(val); err == nil {
			fv.SetBool(b)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, err := strconv.ParseInt(val, 10, fv.Type().Bits()); err == nil {
			fv.SetInt(n)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, err := strconv.ParseUint(val, 10, fv.Type().Bits()); err == nil {
			fv.SetUint(n)
		}
	case reflect.Float32, reflect.Float64:
		if f, err := strconv.ParseFloat(val, fv.Type().Bits()); err == nil {
			fv.SetFloat(f)
		}
	}
}

func registerTyped[In, Out any](app *App, method, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	var inZero In
	var outZero Out
	op := &registeredOp{
		Method:  method,
		Path:    path,
		InType:  reflect.TypeOf(inZero),
		OutType: reflect.TypeOf(outZero),
	}
	for _, o := range opts {
		o(op)
	}

	// The op's stable identity, resolved once (after opts) and handed to the
	// authorizer on every invoke — REST and MCP alike.
	meta := Op{Method: op.Method, Path: op.Path, OperationID: opName(op)}

	// The transport-agnostic core: decode raw JSON args → In, validate, authorize,
	// run fn, return Out (or a literal nil for a void result). REST and MCP both
	// call THIS — one handler, many projections. A nil *Out becomes a nil `any`.
	op.invoke = func(ctx context.Context, rawIn []byte, query, path map[string]string) (any, error) {
		var in In
		if len(rawIn) > 0 {
			if err := jsonenc.Unmarshal(rawIn, &in); err != nil {
				return nil, ErrBadRequest("invalid json body: " + err.Error())
			}
		}
		// The three sources bind in increasing authority: body, then query, then
		// path. Query beats the body because it is part of the URL; path beats
		// query because it is the part the router MATCHED on.
		//
		// The URL is the addressing authority: PATCH /users/acme/bob updates
		// acme/bob whatever the body claims. This is also what keeps the authorizer
		// honest — it runs below on this same decoded value, so the target
		// authorized is the target the URL named, and a body cannot smuggle a
		// different one past it.
		bindURL(&in, query)
		bindURL(&in, path)
		if err := validate(&in); err != nil {
			return nil, ErrBadRequest(err.Error())
		}
		// Authorize the DECODED input — the exact value the handler will bind — so
		// the decision cannot diverge from execution. Runs for REST and MCP alike.
		if auth := app.authorizer; auth != nil {
			if err := auth(ctx, meta, &in); err != nil {
				return nil, err
			}
		}
		out, err := fn(ctx, &in)
		if err != nil {
			return nil, err
		}
		if out == nil {
			return nil, nil
		}
		return out, nil
	}
	app.ops = append(app.ops, op)

	handler := func(c fiber.Ctx) error {
		var body []byte
		if method != "GET" && method != "HEAD" {
			body = c.Body()
		}
		var path map[string]string
		if names := c.Route().Params; len(names) > 0 {
			path = make(map[string]string, len(names))
			for _, n := range names {
				path[n] = c.Params(n)
			}
		}
		// The query string is the OTHER half of the URL. Without it a typed GET
		// could address a collection but never filter it, which is most of a read
		// API — so every route that carries `?q=` had to stay an untyped handler,
		// invisible to OpenAPI and MCP. Reading it here is what makes those routes
		// expressible as ops.
		out, err := op.invoke(callerContext(c), body, c.Queries(), path)
		if err != nil {
			return err
		}
		if out == nil {
			c.Status(204)
			return nil
		}
		return c.JSON(out)
	}
	app.fiber.Add([]string{method}, path, handler)
}
