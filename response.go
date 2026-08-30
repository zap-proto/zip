package zip

import (
	"errors"
	"reflect"
	"strconv"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// The answers that are NOT the success answer.
//
// A typed op says one thing: (*Out, error) — a value at the success status, or a
// fault. Two answers do not fit that shape, and until now neither could be
// declared at all:
//
//   - A REDIRECT, whose answer is a LOCATION and not a body. There was no seam
//     for one anywhere on the typed path: the handler returns (*Out, error), the
//     REST boundary writes a status and JSON, the error renderer writes a status
//     and JSON, and [WithStatus] refuses a 3xx. Nothing could set a header — so
//     every OAuth leg that hands a browser to its provider stayed an UNTYPED
//     route, invisible to the document, the tool list, the CLI and the call
//     plane — the whole cost of the escape hatch, paid by every one of them for
//     one missing header.
//
//   - A non-2xx the CONTRACT wants to NAME: 402 when a spend cap is hit, 409
//     when a step is blocked, 503 when an upstream is down. A handler could
//     always RETURN one ([ErrConflict] and friends, rendered by errorHandler),
//     but the document declared only the success status — so a service's own
//     vocabulary of refusals reached no generated SDK, and every one of them
//     read a deliberate 409 as an unexpected error.
//
// Both are declared as what they are — one more RESPONSE on the op — and neither
// relaxes WithStatus. WithStatus stays 2xx-only on purpose: the status a
// SUCCESSFUL op answers with is one fact, and a second option free to disagree
// about it is the failure this file exists to avoid, not to spread. The two
// halves each still come from one place: the document reads op.Responses, and
// the wire sends what the handler returned.

// WithResponse declares one MORE status this op answers, beyond the success
// status [WithStatus] names: a 3xx it redirects with, or a 4xx/5xx it refuses
// with.
//
//	zip.Get(v1, "/integrations/:provider/start", start, zip.WithResponse(302))
//	zip.Post(v1, "/steps/:id/run", run, zip.WithResponse(409), zip.WithResponse(402))
//
// It is on the op for the same reason WithStatus is: a status is a CONTRACT
// detail, and the contract is what the registry projects. The document's
// responses object gains the code — a 3xx with the Location header it answers
// with, a 4xx/5xx with the error envelope every zip route already writes — so a
// generated SDK, a reader and the wire agree about what this op can say. A
// refusal that only ever existed at run time was not part of the contract.
//
// Declaring it does not MAKE it happen, and nothing here fires on its own: the
// wire still carries what the handler returned — a [Redirect] for the 3xx, an
// [HTTPError] for the rest. One place says what an op CAN answer; another says
// what it DID.
//
// Repeats accumulate and a duplicate is dropped, so an op may name several. A
// 2xx panics: the success status is WithStatus, and two options free to disagree
// about it is exactly the split this vocabulary exists to close.
func WithResponse(code int) OpOption {
	if code < 300 || code > 599 {
		panic("zip: WithResponse wants a 3xx redirect or a 4xx/5xx refusal — the success status is zip.WithStatus")
	}
	return func(op *registeredOp) {
		for _, have := range op.Responses {
			if have == code {
				return
			}
		}
		op.Responses = append(op.Responses, code)
	}
}

// Redirect is the answer an op gives when the answer is WHERE, not what: the
// caller is sent to Location, and there is no body to send.
//
// A handler returns it as its error, because (*Out, error) is the whole of what
// a typed handler can say and a redirect is not an Out:
//
//	func start(ctx context.Context, in *StartIn) (*StartOut, error) {
//	    return nil, &zip.Redirect{Location: provider.AuthURL(in.Provider)}
//	}
//
// Returning it through the error channel is not a pun on failure — it is the one
// seam a typed handler has for "do not send my Out", the same seam [ErrNotFound]
// uses. The renderer knows the difference: a redirect writes the status and the
// Location header and NO body, ahead of every fault branch.
//
// Declare it too — zip.WithResponse(302) — so the document, and therefore every
// generated SDK, knows this op can answer with a location. The value and the
// declaration are the same two halves every status has here: what the op DID
// and what it CAN do.
//
// It is an HTTP notion, so it lands where HTTP is. The op-call plane and
// zip.Call report it as its own status (302, not a collapsed 500) with the
// location in the message; an MCP tools/call has neither status nor headers, so
// the location IS the result there; a CLI reports it, local or remote, as this
// same value. The untyped surface spells the same header with [Ctx.Redirect],
// through the same fiber writer.
type Redirect struct {
	// Status is the 3xx code. Zero means 302 Found — the same "0 is the
	// default" reading [HTTPError.Status] and op.Status already have.
	Status int

	// Location is where the caller is sent, byte for byte as given: it is a URL
	// the handler built, and re-encoding one is how a signed callback loses its
	// signature.
	//
	// A redirect with no location is not followable, so it is refused as the
	// server bug it is rather than sent as a status no client can act on.
	Location string
}

// Error carries the redirect through the handler's error channel, and is what
// every projection without a Location header falls back to reading.
func (r *Redirect) Error() string {
	s := strconv.Itoa(r.code()) + " " + statusText(r.code())
	if r.Location == "" {
		return s + ": no location"
	}
	return s + ": " + r.Location
}

// Unwrap is what keeps the STATUS legible to the planes that carry a status but
// not a header. The op-call plane encodes whatever asHTTPError finds, so without
// this a redirect crossed it as a 500 — the one thing that plane exists to
// preserve, lost on the one answer that is not a fault. With it, a caller's
// errors.As sees 302 and can read the location off the message.
func (r *Redirect) Unwrap() error {
	return &HTTPError{Status: r.code(), Code: "redirect", Msg: r.Error()}
}

// code is the status this redirect sends. One normalizer, so the wire, the
// message and every projection agree about what a zero Status means.
func (r *Redirect) code() int {
	if r.Status == 0 {
		return 302
	}
	return r.Status
}

// followable reports whether this redirect is one a client can act on. A 3xx
// without a location, or a location under a status that does not mean "go here",
// is a programming error — and answering it anyway would be a response no client
// can follow and no document describes.
func (r *Redirect) followable() bool {
	return r.Location != "" && r.code() >= 300 && r.code() <= 399
}

// send writes the redirect through fiber's own redirect writer — the SAME one
// [Ctx.Redirect] uses, so a redirect has one implementation whichever surface
// asked for it. An unfollowable one is rendered as the fault it is.
func (r *Redirect) send(c fiber.Ctx) error {
	if !r.followable() {
		return errorHandler(c, ErrInternal("zip: a redirect wants a 3xx status and a location"))
	}
	return c.Redirect().Status(r.code()).To(r.Location)
}

// text is the redirect as DATA, for a projection that carries neither a status
// nor a header: an MCP tools/call has only content, so the location is the whole
// answer and it arrives as a value to read rather than prose to parse. The
// message is the fallback, so there is always something to say.
func (r *Redirect) text() string {
	if b, err := jsonenc.Marshal(redirectValue{Status: r.code(), Location: r.Location}); err == nil {
		return string(b)
	}
	return r.Error()
}

// redirectValue is the wire shape of [Redirect] where a redirect is data. Named
// rather than a map so the two fields come out in one order every time.
type redirectValue struct {
	Status   int    `json:"status"`
	Location string `json:"location"`
}

// redirectOf is the ONE predicate for "this error carries a redirect", so the
// renderer, the MCP projection and anything else asking read the same answer.
// Whether it can be SENT is [Redirect.followable] — a separate question, asked
// where a broken one has to be reported rather than silently dropped.
func redirectOf(err error) (*Redirect, bool) {
	var r *Redirect
	if errors.As(err, &r) && r != nil {
		return r, true
	}
	return nil, false
}

// locationHeader names where a redirect points. One spelling: the boundary sets
// it and the CLI's remote invoker reads it back off the wire.
const locationHeader = fiber.HeaderLocation

// errorShape is the body a declared refusal carries: the envelope errorHandler
// writes for EVERY route, described from the type that writes it rather than
// restated as a literal, so the document cannot drift from the renderer.
var errorShape = reflect.TypeOf(HTTPError{})

// declareResponses adds the op's declared non-2xx responses to the responses
// object the success status already filled. It is additive by construction: an
// op that declares none is byte-identical to what the document said before, and
// a code the success branch already wrote is left alone — one status, one shape.
//
// A 3xx is described by its Location HEADER, because that is its whole answer; a
// 4xx/5xx by the error envelope, because that is what the wire carries. Neither
// invents a body the route does not send.
func declareResponses(opObj map[string]any, op *registeredOp, reg *schemaRegistry) {
	if len(op.Responses) == 0 {
		return
	}
	resp, ok := opObj["responses"].(map[string]any)
	if !ok {
		return
	}
	for _, code := range op.Responses {
		key := strconv.Itoa(code)
		if _, taken := resp[key]; taken {
			continue
		}
		entry := map[string]any{"description": statusText(code)}
		if code < 400 {
			entry["headers"] = map[string]any{locationHeader: map[string]any{
				"description": "where the caller is sent",
				"required":    true,
				"schema":      map[string]any{"type": "string", "format": "uri"},
			}}
		} else {
			entry["content"] = map[string]any{"application/json": map[string]any{
				"schema": schemaOf(errorShape, reg, nil),
			}}
		}
		resp[key] = entry
	}
}
