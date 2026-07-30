package zip

import (
	"cmp"
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
// every projection reads: the REST route, the OpenAPI doc, the MCP tool, the CLI
// command and the by-name call plane all come from this. invoke is the
// transport-agnostic handler core (decode → run → result), so a REST request, an
// MCP tools/call, a command and a zip.Call run the exact same fn.
//
// An UNTYPED route (app.Get(path, func(c *Ctx) error)) appends nothing here, so
// it is in none of those five. That is the whole cost of the escape hatch, and
// the reason a route that can name its input and output should not use it.
type registeredOp struct {
	Method      string
	Path        string
	OperationID string
	Summary     string
	Status      int // the success status; 0 means the default (200, or 204 for a nil Out)
	Tags        []string
	InType      reflect.Type
	OutType     reflect.Type
	invoke      func(ctx context.Context, dec decoder, rawIn []byte, query, path map[string]string) (any, error)
}

// decoder reads a request body into an op's In. It is a PARAMETER rather than a
// property of the op because the encoding belongs to the transport, not to the
// contract: the same op answers a browser over JSON and a sibling service over
// ZAP, and there is still exactly one handler core underneath both.
type decoder func([]byte, any) error

// Get registers a GET typed handler at path, on the App or on any Router of it
// — a Group's prefix is part of the op's path, so a group-structured app
// declares typed ops without spelling its prefix out per route.
func Get[In, Out any](on OpTarget, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(on, "GET", path, fn, opts...)
}

// Post registers a POST typed handler at path.
func Post[In, Out any](on OpTarget, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(on, "POST", path, fn, opts...)
}

// Put registers a PUT typed handler at path.
func Put[In, Out any](on OpTarget, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(on, "PUT", path, fn, opts...)
}

// Patch registers a PATCH typed handler at path.
func Patch[In, Out any](on OpTarget, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(on, "PATCH", path, fn, opts...)
}

// Delete registers a DELETE typed handler at path. A DELETE addresses what it
// deletes with its URL and carries no request body — see [hasBody].
func Delete[In, Out any](on OpTarget, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	registerTyped(on, "DELETE", path, fn, opts...)
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

// WithStatus declares the status a SUCCESSFUL op answers with — 201 for an op
// that creates a resource, 202 for one that accepts work it has not finished.
// Without it an op answers 200, or 204 when its handler returns a nil Out.
//
// It is declared on the op because the status is part of the CONTRACT, and the
// contract is what the registry projects: the OpenAPI document keys its response
// on this code, so a generated SDK expects the status the service actually
// sends. Setting it per request instead — reaching around the framework from
// inside a handler — writes a contract detail into a side channel no projection
// can read, which is how a document comes to say 200 about a route that has
// always answered 201.
//
// The status is an HTTP notion, so it applies to the REST route and the document
// and to nothing else: an MCP tools/call, a CLI command and a zip.Call all carry
// their own outcome and are untouched.
//
// A non-2xx is refused here, at declaration, rather than at request time. An
// error status comes from the error a handler returns ([ErrNotFound] and
// friends); letting a declaration state one too would be two places for one
// fact, free to disagree.
func WithStatus(code int) OpOption {
	if code < 200 || code > 299 {
		panic("zip: WithStatus wants a 2xx success status — an error status is the error a handler returns")
	}
	return func(op *registeredOp) { op.Status = code }
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
//
// A field names itself for the URL with `url:` and opts out with `url:"-"` — see
// [urlFieldName]. That is what makes a route typable when its path parameter and
// one of its body fields are the same word for two different things.
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
		name := urlFieldName(f)
		if name == "-" {
			continue
		}
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

func registerTyped[In, Out any](on OpTarget, method, path string, fn TypedHandler[In, Out], opts ...OpOption) {
	scope := on.OpScope()
	app := scope.App
	// The op's path is the WHOLE path — the group's prefix composed with the
	// leaf, exactly as the router composes it. Every projection keys on
	// op.Path, so a prefix left out here would name a route that does not exist.
	path = joinPath(scope.Prefix, path)

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
	op.invoke = func(ctx context.Context, dec decoder, rawIn []byte, query, path map[string]string) (any, error) {
		var in In
		if len(rawIn) > 0 {
			if err := dec(rawIn, &in); err != nil {
				return nil, ErrBadRequest("invalid body: " + err.Error())
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
	app.registry = append(app.registry, op)

	handler := func(c fiber.Ctx) error {
		// hasBody is THE rule about what a method carries, read here as well as
		// by the document and the CLI's remote invoker. Reading the body for a
		// method the document says has none is how a DELETE came to accept an
		// input no generated client would ever send.
		var body []byte
		if hasBody(method) {
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
		out, err := op.invoke(callerContext(c), jsonenc.Unmarshal, body, c.Queries(), path)
		if err != nil {
			return err
		}
		if out == nil {
			// A void op answers with the status it DECLARED, else the 204 a nil
			// Out has always meant.
			c.Status(cmp.Or(op.Status, 204))
			return nil
		}
		if op.Status != 0 {
			c.Status(op.Status)
		}
		return c.JSON(out)
	}
	// With() middleware composes around the op only when there IS any: wrapping
	// unconditionally would materialise a *Ctx on every typed request to hand to
	// a chain of length zero, and the typed path is measured (see LLM.md).
	if scope.Middleware != nil {
		// core, not handler: the closure must call the op, and `handler` is
		// about to be reassigned to the wrapper that contains it.
		core := handler
		handler = toFiberHandler(app, scope.Middleware(func(c *Ctx) error { return core(c.fc) }))
	}
	app.fiber.Add([]string{method}, path, handler)
}
