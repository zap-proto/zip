package zip

import (
	"context"
	"strconv"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/fiber/v3"
)

// Who is calling — two different questions, answered by two different
// authorities, and never by the caller itself.
//
//	WHO the request is for   — the gateway's assertion, carried in the identity
//	                           headers below. It was minted upstream, at the
//	                           edge, against an IdP; a service forwards it
//	                           unchanged (see Ctx.Forward) but cannot mint one.
//	WHAT is calling          — the kernel's attestation, read off the unix
//	                           socket as the peer credential (see Peer). It
//	                           cannot be set, spoofed or forwarded by the peer,
//	                           because the peer never sends it: the kernel
//	                           answers on its behalf.
//
// The second is why a service-to-service call over a unix socket needs no
// credential of its own. An argv flag, an environment variable or a shared
// secret would all be things the caller states about itself; SO_PEERCRED is
// the one thing about a caller that the caller does not get to say.

// The gateway-injected identity headers, named once. Ctx.Org and friends read
// these, Ctx.Forward propagates exactly these, and nothing else in zip spells
// them.
const (
	HeaderOrg       = "X-Org-Id"
	HeaderUser      = "X-User-Id"
	HeaderUserEmail = "X-User-Email"
	HeaderUserAdmin = "X-User-IsAdmin"
	HeaderRequestID = "X-Request-Id"
)

// identityHeaders is the set Call forwards — the caller's identity and the
// request id that ties the two hops together in the logs. Deliberately short:
// anything else a callee needs is an argument, and a header that is not
// identity has no business travelling implicitly.
var identityHeaders = [...]string{HeaderOrg, HeaderUser, HeaderUserEmail, HeaderUserAdmin, HeaderRequestID}

// callerKey carries the live request into a handler's context. The REQUEST is
// attached rather than a copy of what a handler might want from it, because
// copying five headers onto every typed request to serve the few that forward
// them is the wrong trade — this way the cost is one small value and the reads
// are lazy.
type callerKey struct{}

// callerContext binds the in-flight request to the context typed handlers
// receive, so the two callers of an op — the REST route and the op-call plane —
// both hand the handler a context that can answer who is calling.
//
// The returned context is valid for the life of the request only: both
// transports reuse the underlying RequestCtx for the next request on the same
// connection.
func callerContext(fc fiber.Ctx) context.Context {
	rc := fc.RequestCtx()
	if rc == nil {
		return fc.Context()
	}
	return context.WithValue(fc.Context(), callerKey{}, rc)
}

// requestOf recovers the in-flight request from a handler's context.
func requestOf(ctx context.Context) *fasthttp.RequestCtx {
	rc, _ := ctx.Value(callerKey{}).(*fasthttp.RequestCtx)
	return rc
}

// Forward returns a context that carries this request's identity onward, so a
// [Call] made with it reaches the next service as the same caller. Use it when
// an untyped handler calls another app; a typed handler's ctx already carries
// it.
//
//	out, err := zip.Call[In, Out](c.Forward(), conn, "flags_bool", &in)
//
// This is propagation, not minting: the headers are the gateway's assertion,
// passed along exactly as received.
func (c *Ctx) Forward() context.Context { return callerContext(c.fc) }

// forwardIdentity copies the caller's identity onto an outbound request. A ctx
// with no request behind it (a background job, a test) forwards nothing, which
// is the honest answer — an unattributed call should look unattributed rather
// than borrow the identity of whatever ran last.
func forwardIdentity(ctx context.Context, req *fasthttp.Request) {
	rc := requestOf(ctx)
	if rc == nil {
		return
	}
	for _, h := range identityHeaders {
		if v := rc.Request.Header.Peek(h); len(v) > 0 {
			req.Header.SetBytesV(h, v)
		}
	}
}

// Caller is the gateway's assertion about who a request is for, read as one
// value. It is what [Ctx.Forward] propagates and what [Call] carries.
//
// Empty fields mean the gateway said nothing — local dev, a direct ingress, or
// a call with no request behind it. Treat empty as unauthenticated, never as
// permitted.
type Caller struct {
	Org       string
	User      string
	Email     string
	Admin     bool
	RequestID string
}

// CallerOf returns the identity forwarded with this call. It is the
// context-shaped read of the identity an untyped handler gets field by field
// off its [Ctx] — one accessor per surface, over the same headers:
//
//	func listFlags(ctx context.Context, in *ListIn) (*ListOut, error) {
//	    org := zip.CallerOf(ctx).Org
//	    if org == "" {
//	        return nil, zip.ErrUnauthorized("no org")
//	    }
//	    ...
//	}
func CallerOf(ctx context.Context) Caller {
	rc := requestOf(ctx)
	if rc == nil {
		return Caller{}
	}
	h := &rc.Request.Header
	return Caller{
		Org:       string(h.Peek(HeaderOrg)),
		User:      string(h.Peek(HeaderUser)),
		Email:     string(h.Peek(HeaderUserEmail)),
		Admin:     string(h.Peek(HeaderUserAdmin)) == "true",
		RequestID: string(h.Peek(HeaderRequestID)),
	}
}

// Peer is the kernel's attestation of the process at the other end of a unix
// socket: what it is, not what it claims. Read it to decide whether a caller
// may reach an op at all — a coarse, infrastructure-level gate under the
// per-user authorization that [Authorizer] performs on the decoded input.
type Peer struct {
	PID int
	UID int
	GID int
}

// String renders a peer for a log line.
func (p *Peer) String() string {
	if p == nil {
		return "peer(unknown)"
	}
	return "pid=" + strconv.Itoa(p.PID) + " uid=" + strconv.Itoa(p.UID) + " gid=" + strconv.Itoa(p.GID)
}

// PeerOf returns the credential of the process that made this call, or nil
// when there is nothing to attest — the request arrived over tcp, or the host
// OS does not report peer credentials (see peerOf).
//
// It is the typed-handler counterpart of [Ctx.Peer]: a typed op reads it off
// the ctx it was handed.
//
//	if p := zip.PeerOf(ctx); p == nil || p.UID != wantUID {
//	    return nil, zip.ErrForbidden("not a fleet peer")
//	}
func PeerOf(ctx context.Context) *Peer {
	rc := requestOf(ctx)
	if rc == nil {
		return nil
	}
	return peerOf(rc.Conn())
}

// Peer returns the credential of the process that made this request, or nil.
// The untyped-handler counterpart of [PeerOf].
func (c *Ctx) Peer() *Peer {
	rc := c.fc.RequestCtx()
	if rc == nil {
		return nil
	}
	return peerOf(rc.Conn())
}
