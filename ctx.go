package zip

import (
	"bufio"
	"context"
	"io"
	"net/url"
	"strings"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip/internal/jsonenc"
)

// Ctx wraps fiber.Ctx and adds the Hanzo identity surface (Org/User/Email
// from gateway-minted X-* headers per HIP-0026), a per-request luxfi/log
// logger, and typed Deps access.
type Ctx struct {
	fc  fiber.Ctx
	app *App
	log luxlog.Logger
}

// Fiber returns the underlying fiber.Ctx for one-off escape into Fiber-only APIs.
func (c *Ctx) Fiber() fiber.Ctx { return c.fc }

// App returns the parent App.
func (c *Ctx) App() *App { return c.app }

// Context returns the standard context.Context (deadline + cancellation).
func (c *Ctx) Context() context.Context { return c.fc.Context() }

// Log returns the request-scoped logger. Middleware that adds request_id,
// org, user, etc. via Locals can enrich this by calling SetLog.
func (c *Ctx) Log() luxlog.Logger { return c.log }

// SetLog replaces the request logger (typically by middleware that wants
// to attach request-id / org / user fields).
func (c *Ctx) SetLog(l luxlog.Logger) { c.log = l }

// ----- Hanzo identity (HIP-0026 / gateway X-* headers) ---------------------

// Org returns the X-Org-Id from the JWT-validated gateway. Empty when
// no gateway is in front (local dev / direct ingress).
func (c *Ctx) Org() string { return c.fc.Get(HeaderOrg) }

// Project returns the X-Project-Id from the JWT-validated gateway. It narrows
// the org, and is empty for a request scoped to the org as a whole.
func (c *Ctx) Project() string { return c.fc.Get(HeaderProject) }

// User returns the X-User-Id from the JWT-validated gateway.
func (c *Ctx) User() string { return c.fc.Get(HeaderUser) }

// UserName returns the X-User-Name from the JWT-validated gateway — the minted
// name, where User is the opaque id.
func (c *Ctx) UserName() string { return c.fc.Get(HeaderUserName) }

// UserEmail returns the X-User-Email from the JWT-validated gateway.
func (c *Ctx) UserEmail() string { return c.fc.Get(HeaderUserEmail) }

// UserOwner returns the X-User-Owner gateway claim: the org this principal
// belongs to. A deployment reserving one org for platform operators gates its
// cross-tenant surfaces on this and never on IsOrgAdmin.
func (c *Ctx) UserOwner() string { return c.fc.Get(HeaderUserOwner) }

// IsAdmin returns the X-User-IsAdmin gateway claim as a bool.
func (c *Ctx) IsAdmin() bool { return c.fc.Get(HeaderUserAdmin) == "true" }

// IsOrgAdmin returns the X-User-IsOrgAdmin gateway claim: this principal
// administers their OWN org. It is not platform authority — see UserOwner.
func (c *Ctx) IsOrgAdmin() bool { return c.fc.Get(HeaderUserOrgAdmin) == "true" }

// RequestID returns the value of X-Request-Id (set by the RequestID middleware).
func (c *Ctx) RequestID() string { return c.fc.Get(HeaderRequestID) }

// ----- request basics ------------------------------------------------------

// Method returns the request method.
func (c *Ctx) Method() string { return c.fc.Method() }

// Path returns the request path.
func (c *Ctx) Path() string { return c.fc.Path() }

// Param returns a URL path parameter, as the caller wrote it.
func (c *Ctx) Param(name string) string { return segment(c.fc.Params(name)) }

// segment decodes one path parameter.
//
// A URL carries a segment percent-encoded — it has to: a space, a slash, a
// percent or anything outside the unreserved set has no other spelling between
// two slashes. The router matches on the raw text and hands that same text on,
// so without this a handler addressed at /v1/secrets/café is given "caf%C3%A9"
// and looks up a name nobody has. Measured before this existed: every path
// parameter carrying such a character arrived encoded, and no client could make
// one round-trip.
//
// Text that is not valid encoding is passed through rather than refused. A lone
// percent is a malformed URI, but the address it names is still the address the
// router matched, and failing here would turn a lookup that finds nothing into
// a 500.
func segment(s string) string {
	if !strings.Contains(s, "%") {
		return s // the ordinary case: nothing encoded, nothing to do
	}
	if out, err := url.PathUnescape(s); err == nil {
		return out
	}
	return s
}

// Query returns a URL query parameter.
func (c *Ctx) Query(name string) string { return c.fc.Query(name) }

// Header returns a request header.
func (c *Ctx) Header(name string) string { return c.fc.Get(name) }

// Host returns the request Host (authority) from the Host header, port included
// when present. It honors X-Forwarded-Host ONLY when the app is configured to
// trust proxies — which zip does NOT do (there is no TrustProxy knob on
// zip.Config), so a client-supplied X-Forwarded-Host is ignored and cannot spoof
// the value. Used for white-label brand-by-host resolution (see
// middleware.ProductionHeaders); keep it un-trusted-proxy so the Server brand
// cannot be forged from a request header.
func (c *Ctx) Host() string { return c.fc.Host() }

// SetHeader sets a response header.
func (c *Ctx) SetHeader(name, value string) { c.fc.Set(name, value) }

// Body returns the raw request body.
func (c *Ctx) Body() []byte { return c.fc.Body() }

// Bind parses the request body into v based on Content-Type (JSON by
// default) and runs struct-tag validation (required/min/max/minlen/maxlen).
// Returns a *HTTPError(400) when either step fails so handlers can
// return the error directly.
func (c *Ctx) Bind(v any) error {
	if err := c.fc.Bind().Body(v); err != nil {
		return ErrBadRequest("invalid body: " + err.Error())
	}
	if err := validate(v); err != nil {
		return ErrBadRequest(err.Error())
	}
	return nil
}

// BindQuery parses query parameters into v and runs validation.
func (c *Ctx) BindQuery(v any) error {
	if err := c.fc.Bind().Query(v); err != nil {
		return ErrBadRequest("invalid query: " + err.Error())
	}
	if err := validate(v); err != nil {
		return ErrBadRequest(err.Error())
	}
	return nil
}

// BindURI parses URL params into v and runs validation.
func (c *Ctx) BindURI(v any) error {
	if err := c.fc.Bind().URI(v); err != nil {
		return ErrBadRequest("invalid uri: " + err.Error())
	}
	if err := validate(v); err != nil {
		return ErrBadRequest(err.Error())
	}
	return nil
}

// ----- response writers ----------------------------------------------------

// Status sets the response status. Chains.
func (c *Ctx) Status(code int) *Ctx { c.fc.Status(code); return c }

// JSON writes the value as JSON with status code.
func (c *Ctx) JSON(code int, v any) error {
	c.fc.Status(code)
	return c.fc.JSON(v)
}

// String writes a plain-text response.
func (c *Ctx) String(code int, s string) error {
	c.fc.Status(code)
	return c.fc.SendString(s)
}

// Bytes writes raw bytes.
func (c *Ctx) Bytes(code int, b []byte) error {
	c.fc.Status(code)
	return c.fc.Send(b)
}

// NoContent writes the status code with no body.
func (c *Ctx) NoContent(code int) error {
	c.fc.Status(code)
	return nil
}

// SendStream streams data from r to the client (e.g. for SSE).
func (c *Ctx) SendStream(r io.Reader) error {
	return c.fc.SendStream(r)
}

// SendStreamWriter writes streaming output via a bufio.Writer (Server-Sent
// Events / chunked transfer). Forwards to fiber.Ctx.SendStreamWriter.
func (c *Ctx) SendStreamWriter(fn func(w *bufio.Writer)) error {
	return c.fc.SendStreamWriter(fn)
}

// Locals returns or sets a per-request value.
func (c *Ctx) Locals(key any, value ...any) any {
	return c.fc.Locals(key, value...)
}

// Next yields to the next handler in the chain. Use sparingly from
// zip middleware — middleware bodies usually call c.Continue() at the
// end, not Next() mid-handler.
func (c *Ctx) Next() error { return c.fc.Next() }

// Continue is an alias for Next() with the standard middleware idiom.
func (c *Ctx) Continue() error { return c.fc.Next() }

// =============================================================================
// Errors — handlers return one of these to control status code
// =============================================================================

// HTTPError is the canonical error type zip understands. Returning one is how
// a handler refuses; how the refusal is WRITTEN belongs to the address it
// happened at, and lives in problem.go.
//
// The three members map onto RFC 9457 exactly: Status is `status`, Msg is
// `detail`, and Code is the extension member a client dispatches on. They map
// onto RFC 6749 §5.2 just as exactly — Code is `error`, Msg is
// `error_description` — which is why an OAuth endpoint needs no error type of
// its own. One value, two vocabularies, chosen by the endpoint.
type HTTPError struct {
	Status int
	Code   string
	Msg    string

	// Detail is what a refusal carries BESIDES its message, and it exists
	// because a typed op's only way to refuse is to RETURN an error — so
	// without it, a route whose non-2xx answer has a shape cannot be a typed op
	// at all.
	//
	// That is not hypothetical. Six subsystems route around its absence: a
	// prepaid gate answers 402 naming the cap and the balance, a plugin build
	// answers 422 carrying the diagnostics that say why it failed, a degraded
	// probe answers 503 carrying its report. Each was left untyped for this one
	// reason — losing its schema, its prose, its MCP tool and its CLI command —
	// and three separate envelopes were hand-rolled to carry a body beside an
	// error that could not.
	//
	// It is an EXTENSION in the RFC 9457 sense: the members a problem document
	// may carry beyond its own. Rendered by MERGING rather than nesting, so a
	// reader sees one object instead of a body filed under a key it has to know
	// to look in — and the document's own members are written last, so a domain
	// key called status cannot displace the refusal's own.
	Detail map[string]any
}

// With attaches extension members to a refusal and returns it, so an op refuses
// in one expression:
//
//	return zip.ErrPaymentRequired("spend cap exceeded").
//		With(map[string]any{"cap": 5000, "spent": 5127})
//
// It MERGES rather than replaces: a gate naming the cap and a meter naming the
// ledger are two facts about one refusal.
func (e *HTTPError) With(detail map[string]any) *HTTPError {
	if len(detail) == 0 {
		return e
	}
	if e.Detail == nil {
		e.Detail = make(map[string]any, len(detail))
	}
	for k, v := range detail {
		e.Detail[k] = v
	}
	return e
}

// MarshalJSON writes the RFC 9457 problem document, with no `instance`: a value
// marshalled on its own has no occurrence to name. Served through an address,
// the same document gains one — see [HTTPError.problem].
func (e *HTTPError) MarshalJSON() ([]byte, error) {
	return jsonenc.Marshal(e.problem())
}

func (e *HTTPError) Error() string { return e.Msg }

// Errorf builds an HTTPError with the given status and message.
func Errorf(status int, format string, args ...any) *HTTPError {
	return &HTTPError{Status: status, Msg: sprintf(format, args...)}
}

// Common shortcuts.
func ErrBadRequest(msg string) *HTTPError   { return &HTTPError{Status: 400, Msg: msg} }
func ErrUnauthorized(msg string) *HTTPError { return &HTTPError{Status: 401, Msg: msg} }
func ErrForbidden(msg string) *HTTPError    { return &HTTPError{Status: 403, Msg: msg} }
func ErrNotFound(msg string) *HTTPError     { return &HTTPError{Status: 404, Msg: msg} }
func ErrConflict(msg string) *HTTPError     { return &HTTPError{Status: 409, Msg: msg} }

// ErrPaymentRequired is the prepaid gate's refusal, and it is a shortcut here
// because the surfaces that refuse this way are exactly the ones that must say
// WHY — a cap, a balance, a ledger — which is what [HTTPError.With] carries.
func ErrPaymentRequired(msg string) *HTTPError { return &HTTPError{Status: 402, Msg: msg} }

// ErrUnprocessable is the refusal for a request that parsed and cannot be acted
// on — source that will not build, a document failing its own schema. Its
// diagnostics belong ON the refusal rather than beside it.
func ErrUnprocessable(msg string) *HTTPError { return &HTTPError{Status: 422, Msg: msg} }
func ErrInternal(msg string) *HTTPError      { return &HTTPError{Status: 500, Msg: msg} }

// Redirect sends an HTTP redirect to location with the given status code.
func (c *Ctx) Redirect(code int, location string) error {
	return c.fc.Redirect().Status(code).To(location)
}

// SetContext replaces the request's context.Context — the boundary idiom: a
// middleware derives a request-scoped context (values, gates, deadlines) ONCE
// and every later c.Context() returns it. One context per request, one setter.
func (c *Ctx) SetContext(ctx context.Context) {
	c.fc.SetContext(ctx)
}
