package zip

import (
	"cmp"
	"context"
	"fmt"
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
	// ResponseHeaders are the header names this op may set on its answer,
	// declared. A name not in this set is refused rather than written — the
	// document publishes exactly this list, and a header a client relies on is
	// part of the contract. See [HeaderCoder].
	ResponseHeaders []string

	// readsHeaders is whether the input declares any `header:` field, computed
	// ONCE at registration. Without it every request would walk the input type
	// by reflection to discover that it declares none, which is the answer for
	// almost every op.
	readsHeaders bool

	// Statuses are the success codes this op may answer with, declared. Empty
	// means the default (200, or 204 for a nil Out). More than one means the
	// handler CHOOSES per request — see [WithStatus] and [StatusCoder].
	Statuses []int
	Tags     []string
	InType   reflect.Type
	OutType  reflect.Type
	// Origin names the app this op was DECLARED in when it arrived here through
	// Graft — empty for an op this app registered itself. It qualifies the
	// op's named types in the composed document (see schemaRegistry.origin), so
	// two children may both call a type Application without one overwriting the
	// other. It is the child's own AppName, never a prefix or a deployment
	// choice: it says WHO declared the type, which is a property of the code.
	Origin string
	invoke func(ctx context.Context, dec decoder, rawIn []byte, query, path map[string]string, header func(string) string) (any, error)
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
// # Declaring a non-2xx
//
// A declared non-2xx is for an op that answers a failure with its OWN TYPED
// BODY — a 409 carrying the conflicting record, a 404 carrying what was
// searched for. It is not a second way to spell an error: [ErrNotFound] and
// friends remain how a handler returns a failure, and they render the standard
// {status, code, error} envelope every client already parses.
//
// The difference is which body reaches the wire. An error returns the envelope;
// a declared status returns the op's Out type, described in the document under
// that code like any other response. Reach for the error unless the caller
// genuinely needs a typed body it cannot get from the envelope — two ways to
// spell the same failure is exactly the drift this package spends its doc
// comments avoiding.
func WithStatus(codes ...int) OpOption {
	if len(codes) == 0 {
		panic("zip: WithStatus needs at least one status")
	}
	seen := map[int]bool{}
	for _, code := range codes {
		if code < 100 || code > 599 {
			panic("zip: WithStatus wants an HTTP status")
		}
		if seen[code] {
			panic("zip: WithStatus: duplicate status")
		}
		seen[code] = true
	}
	return func(op *registeredOp) { op.Statuses = append([]int(nil), codes...) }
}

// StatusCoder is how an op with MORE THAN ONE declared success status says which
// one this answer is. The OUTPUT VALUE states it:
//
//	func (r *CreateOut) StatusCode() int {
//	    if r.Existed { return 200 }
//	    return 201
//	}
//
//	zip.Post(app, "/v1/things", create, zip.WithStatus(200, 201))
//
// The status is a property of the ANSWER, so it rides the value the handler
// already returns. It is deliberately not a mutable slot on the request: a slot
// is a side channel, and a side channel is invisible to every projection, which
// is the whole defect this closes.
//
// A code the op did not declare is refused at the seam rather than written to
// the wire, because the document publishes exactly the declared set and a
// generated client expects nothing else.
type StatusCoder interface{ StatusCode() int }

// statusOf is the code an answer carries: what the value states if it states
// anything, else the op's first declared status, else the default.
func statusOf(op *registeredOp, out any) (int, error) {
	if sc, ok := out.(StatusCoder); ok {
		got := sc.StatusCode()
		for _, declared := range op.Statuses {
			if declared == got {
				return got, nil
			}
		}
		// An ERROR, not a panic: this is a runtime value, so it cannot be caught
		// at boot, and a panic on a serving goroutine is a worse answer than a
		// refusal the error chain renders. The document publishes exactly the
		// declared set, so sending anything else tells the caller one thing and
		// hands them another.
		return 0, ErrInternal(fmt.Sprintf("%s %s answered %d, which it does not declare — "+
			"declare it with zip.WithStatus(%d) so the document, the SDKs and the CLI publish it",
			op.Method, op.Path, got, got))
	}
	if len(op.Statuses) > 0 {
		return op.Statuses[0], nil
	}
	return 0, nil
}

// WithResponseHeader declares the headers this op may set on its answer.
//
//	zip.Get(app, "/v1/report", report,
//	    zip.WithResponseHeader("Cache-Control"))
//
// It is the response half of [WithStatus], and it exists for the same reason: a
// response header a caller can rely on — a cache directive, a payment challenge,
// a Set-Cookie — is part of the contract, so it belongs in the document rather
// than in a context slot some middleware writes on the way out. That slot is
// invisible to OpenAPI, to generated SDKs and to the tool schema, which is
// exactly how a browser session cookie came to be set by a route no projection
// described.
//
// The VALUE is stated by the answer, via [HeaderCoder].
func WithResponseHeader(names ...string) OpOption {
	if len(names) == 0 {
		panic("zip: WithResponseHeader needs at least one header name")
	}
	return func(op *registeredOp) { op.ResponseHeaders = append(op.ResponseHeaders, names...) }
}

// HeaderCoder is how an answer states the response headers it carries. The
// headers ride the value the handler already returns, so there is no mutable
// slot on the request:
//
//	func (r *ReportOut) ResponseHeaders() map[string]string {
//	    return map[string]string{"Cache-Control": "no-store"}
//	}
//
// A name the op did not declare is refused at the seam. The document publishes
// the declared set, so writing anything else tells a caller one thing and sends
// another — the same rule an undeclared status obeys.
//
// # Transports without a response of their own
//
// The headers are written wherever the op OWNS the response: over REST, and over
// the by-name call plane, where one request carries one op. They are NOT written
// over MCP, because one HTTP response there carries a whole JSON-RPC envelope
// that may hold several results, and there is no per-op response to put them on.
// That is a defined meaning, not an oversight: the declared set still appears in
// the tool schema, so an agent can see what the op says about itself, and the
// answer's value is identical on every transport.
type HeaderCoder interface{ ResponseHeaders() map[string]string }

// responseHeadersOf is the header set an answer carries, checked against what
// the op declared.
func responseHeadersOf(op *registeredOp, out any) (map[string]string, error) {
	hc, ok := out.(HeaderCoder)
	if !ok {
		return nil, nil
	}
	got := hc.ResponseHeaders()
	if len(got) == 0 {
		return nil, nil
	}
	for name := range got {
		declared := false
		for _, d := range op.ResponseHeaders {
			if strings.EqualFold(d, name) {
				declared = true
				break
			}
		}
		if !declared {
			return nil, ErrInternal(fmt.Sprintf("%s %s set response header %q, which it does not declare — "+
				"declare it with zip.WithResponseHeader(%q) so the document, the SDKs and the tool schema publish it",
				op.Method, op.Path, name, name))
		}
	}
	return got, nil
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
// bindHeaders copies declared header values onto the decoded input. Only fields
// carrying a `header:` tag are eligible: an op reads the headers it DECLARES and
// no others, so the document and the input can never disagree about which ones
// matter.
//
// get is a function rather than a map because the transports answer differently
// and all of them answer honestly: over HTTP it reads the request's headers;
// over MCP and the call plane, where the op's arguments arrived as a JSON object
// and there is no per-op request, it reads nothing and the field keeps whatever
// the arguments supplied. That is the same shape [CallerOf] uses, and the reason
// neither of them panics on a transport that has no HTTP request.
func bindHeaders(in any, get func(string) string) {
	if get == nil {
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
	for _, f := range wireFields(v.Type()) {
		name := headerFieldName(f)
		if name == "" {
			continue
		}
		fv := v.FieldByIndex(f.Index)
		if !fv.CanSet() {
			continue
		}
		if got := get(name); got != "" {
			setScalar(fv, got)
		}
	}
}

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
	for _, f := range wireFields(v.Type()) {
		name := urlFieldName(f)
		if name == "-" {
			continue
		}
		// FieldByIndex, because wireFields reaches through embedding: the index is
		// the path to the field, which is just [i] for the type's own fields.
		fv := v.FieldByIndex(f.Index)
		if !fv.CanSet() {
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
	op.readsHeaders = len(headerFields(op.InType)) > 0

	// The op's stable identity, resolved once (after opts) and handed to the
	// authorizer on every invoke — REST and MCP alike.
	meta := Op{Method: op.Method, Path: op.Path, OperationID: opName(op)}

	// The transport-agnostic core: decode raw JSON args → In, validate, authorize,
	// run fn, return Out (or a literal nil for a void result). REST and MCP both
	// call THIS — one handler, many projections. A nil *Out becomes a nil `any`.
	op.invoke = func(ctx context.Context, dec decoder, rawIn []byte, query, path map[string]string, header func(string) string) (any, error) {
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
		// Headers first, then the URL: the URL is the addressing authority and
		// must win, exactly as it does over the body. Skipped entirely unless
		// the input declares one, so an op that reads no header pays nothing.
		if op.readsHeaders {
			bindHeaders(&in, header)
		}
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
		// The header reader is built only for an op that declares one: the
		// closure captures c, so it is per-request, and almost every op reads no
		// header at all.
		var header func(string) string
		if op.readsHeaders {
			header = func(k string) string { return c.Get(k) }
		}
		out, err := op.invoke(callerContext(c), jsonenc.Unmarshal, body, c.Queries(), path, header)
		if err != nil {
			return err
		}
		if out == nil {
			// A void op answers with the status it DECLARED, else the 204 a nil
			// Out has always meant.
			code, serr := statusOf(op, nil)
			if serr != nil {
				return serr
			}
			c.Status(cmp.Or(code, 204))
			return nil
		}
		hdrs, herr := responseHeadersOf(op, out)
		if herr != nil {
			return herr
		}
		for name, v := range hdrs {
			c.Set(name, v)
		}
		code, serr := statusOf(op, out)
		if serr != nil {
			return serr
		}
		if code != 0 {
			c.Status(code)
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
	// ONE entry. The route and the op are the same fact recorded once, so the
	// router and all five projections read the same value and cannot disagree
	// about what exists — which is what makes composition a walk rather than a
	// merge of a router and a registry that were written separately.
	app.addRoute(here(2), route{method: method, path: path, serve: handler, op: op, shadow: scope.Shadow})
}
