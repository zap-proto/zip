package zip

import (
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// The fault plane — a refusal is an ANSWER, and an answer has a body.
//
// zip's body for a refusal is [HTTPError]'s envelope, {status, code, error}, and
// it stays exactly that for every op that declares nothing. But a refusal that
// carries STRUCTURE — which step blocked, which cap was hit, how to clear it —
// had nowhere to put it. The envelope has one string, so the structure was
// flattened into prose the caller had to parse back out, and the document said
// nothing about that status at all. The alternative was to leave the route
// untyped, which costs it the document, the tool list, the command and the
// call plane together, for the sake of one error body.
//
// So a fault is declared on the op like everything else, and projected from the
// registry like everything else: [WithFault] states the status and the shape,
// the OpenAPI document publishes it under that status beside the success
// response, and the handler answers it by returning that body
// ([HTTPError.WithBody]). One value, every projection — a generated SDK gets a
// typed refusal it can react to, an agent's tool result carries the shape, and
// the op-call plane hands a sibling service the same bytes a browser reads.

// fault is one refusal an op DECLARES: the status it answers with, and the type
// of the body that carries it. A nil Type declares the status is answered with
// zip's own envelope, which is what an op returning a plain [ErrNotFound] does.
type fault struct {
	Status int
	Type   reflect.Type
}

// WithFault declares one refusal this op can answer with: the status, and the
// type of the body that carries it.
//
//	zip.Post(app, "/v1/guide/steps/:id/run", run, zip.WithFault[Blocked](409))
//
// and the handler answers that refusal by returning the body:
//
//	return zip.ErrConflict("step 2 is blocked").WithBody(Blocked{Step: 2, …})
//
// Body is the WHOLE response body for that status, not a fragment of the
// envelope: a service whose published error contract is {"error":{"code",
// "message"}} says exactly that, rather than having zip wrap it inside a second
// error object. A Body of `any` — zip.WithFault[any](404) — declares the status
// is answered with zip's envelope, so an op can publish the refusals it makes
// without inventing a type for the ones that need no shape.
//
// The declaration is the SET of refusals the contract admits; the error a
// handler returns is the ONE this request met. They are different facts, which
// is why both name the status — unlike a success status, which the handler has
// no choice about and therefore never restates (see [WithStatus]). Nothing
// checks that a returned body is the declared type: the declaration and the
// handler are written together, exactly as a route pattern and its In are.
//
// A status outside 4xx/5xx is refused here, at declaration, for the same reason
// [WithStatus] refuses a non-2xx — a fault is a refusal, and a success status is
// WithStatus's to state. Declaring one status twice on one op is refused too: a
// response carries one schema, so two shapes under one code is a document that
// cannot be true.
func WithFault[Body any](status int) OpOption {
	if status < 400 || status > 599 {
		panic("zip: WithFault wants a 4xx or 5xx refusal status — a success status is WithStatus's to declare")
	}
	var zero Body
	f := fault{Status: status, Type: reflect.TypeOf(zero)}
	return func(op *registeredOp) {
		for _, have := range op.Faults {
			if have.Status == status {
				panic("zip: WithFault declares status " + strconv.Itoa(status) +
					" twice on one op — a response carries one schema")
			}
		}
		op.Faults = append(op.Faults, f)
	}
}

// WithBody returns a copy of e carrying body as its RESPONSE BODY — the value
// the boundary sends INSTEAD of the envelope, and the shape the op declared with
// [WithFault]. Status, code and message are untouched: they are what the error
// says to a log, a metric and the op-call plane, whatever the body looks like.
//
// A copy, because an error is often a package-level value: decorating a shared
// sentinel in place would answer THIS request by rewriting the one every other
// request shares.
//
// The body is encoded once, here, so every projection carries the same bytes and
// the value cannot change under them afterwards. A body that does not encode
// leaves the refusal exactly as it was — status, code and message intact, no
// body — the same choice bindURL makes for an unparseable value: the refusal is
// still true, and a 500 in place of the 409 the caller earned is a worse answer
// than a missing field.
func (e *HTTPError) WithBody(body any) *HTTPError {
	c := *e
	if b, err := jsonenc.Marshal(body); err == nil {
		c.body = b
	}
	return &c
}

// Body is the declared response body this refusal carries, or nil when it
// answers with the envelope. It survives the op-call plane, so a caller of
// [Call] reads the very bytes a browser would have read (see [remoteError]).
func (e *HTTPError) Body() json.RawMessage { return e.body }

// MarshalJSON is the refusal AS ITS BODY: the declared shape when there is one,
// the envelope when there is not. It lives on the type rather than at the REST
// boundary because a refusal is written in more than one place — zip's renderer,
// a service's own in-band writer under an outer error filter, a log line, a
// value that carries the error inside it — and a body honored by only one of them
// is a shape that vanishes depending on which route answered.
//
// An error with no declared body encodes byte for byte as it always has: the
// same fields, in the same order, under the same tags.
func (e *HTTPError) MarshalJSON() ([]byte, error) {
	if len(e.body) > 0 {
		return e.body, nil
	}
	// envelope sheds this method, so marshalling the fields cannot re-enter it.
	type envelope HTTPError
	return jsonenc.Marshal(envelope(*e))
}

// withRawBody adopts bytes that were already encoded — a body that crossed the
// op-call plane is JSON the callee produced, not a value to marshal again. It is
// VALIDATED here, at the one door bytes arrive through from elsewhere: a body
// that is not JSON is not a body, and adopting one would turn a refusal a caller
// can read into an encoding failure further down.
func (e *HTTPError) withRawBody(body []byte) *HTTPError {
	c := *e
	if json.Valid(body) {
		c.body = body
	}
	return &c
}

// declareFaults publishes an op's declared refusals into its OpenAPI operation
// object: one response per fault, keyed on its own status, with the shape that
// status carries. Same rule the success response follows — the contract is what
// the registry projects — so a generated client can react to a 409 instead of
// parsing its prose, and a reader of the reference learns the refusal exists.
//
// The success response is registered first and keeps its code: a declared fault
// is 4xx/5xx and a success is 2xx, so they cannot collide, and if one ever did
// the answer the op actually sends wins.
func declareFaults(opObj map[string]any, op *registeredOp, reg *schemaRegistry, fields map[string]string) {
	if len(op.Faults) == 0 {
		return
	}
	resp, ok := opObj["responses"].(map[string]any)
	if !ok {
		resp = map[string]any{}
		opObj["responses"] = resp
	}
	for _, f := range op.Faults {
		code := strconv.Itoa(f.Status)
		if _, taken := resp[code]; taken {
			continue
		}
		resp[code] = map[string]any{
			"description": statusText(f.Status),
			"content": map[string]any{
				"application/json": map[string]any{"schema": faultSchema(f.Type, reg, fields)},
			},
		}
	}
}

// faultEnvelope is the type that WRITES zip's default refusal body, so the
// schema published for a fault with no shape of its own is derived from the
// struct on the wire rather than restated beside it. The unexported body field
// is skipped like any unexported field, which is what makes it right to publish
// this type: what a reader sees is exactly what a reader gets.
var faultEnvelope = reflect.TypeOf(HTTPError{})

// faultSchema is the schema of a refusal's body: the declared type, or zip's
// envelope for a fault declared without one.
func faultSchema(t reflect.Type, reg *schemaRegistry, fields map[string]string) map[string]any {
	if t == nil {
		return schemaOf(faultEnvelope, reg, fields)
	}
	return schemaOf(t, reg, fields)
}
