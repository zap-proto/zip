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

// Build constructs and installs a generation without starting a listener: the
// walk runs, every validation runs, the projections are rendered, and the
// router goes live — exactly what [App.Listen] does minus the sockets.
//
// It RETURNS THE VERDICT, which is the whole reason it replaced Prepare. A
// composition can be invalid — two definitions claiming one address, a cycle —
// and the old Prepare returned nothing, so the only way to learn was to start
// a server. Now a codegen step, a test or a wiring file's own main can ask
// whether the program it just wrote is a program, and get every conflict at
// once with both claimants named.
//
// Idempotent for the projections (they render once) and monotonic for the
// freeze. Listen calls it; there is no other way to build.
func (a *App) Build() error {
	a.prepare()
	a.buildMu.Lock()
	g, err := a.build()
	if err == nil {
		a.install(g)
	}
	a.buildMu.Unlock()
	return err
}
