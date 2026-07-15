package zip

import "context"

// Op is the stable identity of a registered typed handler, handed to an
// Authorizer so the decision can key on the operation as well as the input.
// OperationID is the resolved id the OpenAPI document and the MCP tool surface
// share — the explicit WithOperationID, else the method+path default.
type Op struct {
	Method      string
	Path        string
	OperationID string
}

// Authorizer authorizes a decoded, validated typed request at the op-invoke
// seam — the ONE point every projection of a typed handler funnels through. It
// runs after the request is decoded into the op's typed In and validated, and
// BEFORE the handler runs, over REST and MCP alike, so the value it authorizes
// is exactly the value the handler will act on: there is no second parse of the
// body for it to diverge from. in is the *In the handler will receive.
//
// Returning a non-nil error aborts the op before the handler runs, and that
// error is the response — return a zip.Err* (e.g. ErrForbidden) for a clean
// status.
type Authorizer func(ctx context.Context, op Op, in any) error

// Authorize installs fn as the op-invoke authorization hook. It is the op-level
// counterpart to Use: Use wraps the whole request with transport middleware,
// which for a body request sees only the raw bytes; Authorize runs one decision
// on the DECODED typed input of every op, REST and MCP alike — the seam a
// mounted subsystem gates on so the value it authorizes is the value the handler
// binds. Call once while mounting, before Listen. A nil fn clears it (every
// decoded request then runs unauthorized).
func (a *App) Authorize(fn Authorizer) { a.authorizer = fn }

// Prepare installs the deferred projections (the OpenAPI document and the MCP
// tool surface) without starting a listener, so a test can drive them through
// Fiber().Test exactly as a served app exposes them. Listen calls it too; both
// share one guard, so it runs at most once however it is reached.
func (a *App) Prepare() { a.prepare() }
