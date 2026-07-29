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
//
// The set is what a gateway can assert and a callee can act on, which is more
// than a subject: WHOSE tenant (org, project), WHICH person (id, name, email),
// and the two admin scopes — which are distinct authorities and must never
// collapse into one another. Owner names the org a principal belongs to, and a
// deployment that reserves one org for platform operators reads platform sudo
// off THAT and never off IsOrgAdmin, which says only "administers their own
// org". A plane that forwarded one and dropped the other would let a callee
// read "holds an org" as "administers it", or worse, as "administers the
// fleet".
const (
	HeaderOrg          = "X-Org-Id"
	HeaderProject      = "X-Project-Id"
	HeaderUser         = "X-User-Id"
	HeaderUserName     = "X-User-Name"
	HeaderUserEmail    = "X-User-Email"
	HeaderUserOwner    = "X-User-Owner"
	HeaderUserAdmin    = "X-User-IsAdmin"
	HeaderUserOrgAdmin = "X-User-IsOrgAdmin"
	HeaderRequestID    = "X-Request-Id"
)

// identityHeaders is the set Call forwards — the caller's identity and the
// request id that ties the two hops together in the logs. It is exactly the
// list above and nothing beyond it: anything else a callee needs is an
// argument, and a header that is not identity has no business travelling
// implicitly.
//
// Forwarding a SUBSET is the failure this list exists to prevent. A callee
// resolving a billing subject prefers the minted name over the opaque id, and
// one gating a privileged surface reads the owner; drop either from the plane
// and the same handler decides differently depending on whether it was reached
// over REST or over a call — silently, and in the direction that bills or
// admits the wrong principal.
var identityHeaders = [...]string{
	HeaderOrg, HeaderProject,
	HeaderUser, HeaderUserName, HeaderUserEmail, HeaderUserOwner,
	HeaderUserAdmin, HeaderUserOrgAdmin,
	HeaderRequestID,
}

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
// borrow the identity of whatever ran last. A background caller that
// legitimately acts FOR someone says so with [WithCaller], and an inbound
// request always wins over what it said.
func forwardIdentity(ctx context.Context, req *fasthttp.Request) {
	if rc := requestOf(ctx); rc != nil {
		for _, h := range identityHeaders {
			if v := rc.Request.Header.Peek(h); len(v) > 0 {
				req.Header.SetBytesV(h, v)
			}
		}
		return
	}
	stated, ok := ctx.Value(statedKey{}).(Caller)
	if !ok {
		return
	}
	for h, v := range stated.headers() {
		req.Header.Set(h, v)
	}
}

// statedKey carries an explicitly stated caller — see [WithCaller].
type statedKey struct{}

// WithCaller states who a [Call] made with this context acts for, for a caller
// that has no inbound request to propagate.
//
// The background work a service does is not all unattributed. A grant issued
// when an org opens, a meter that debits after the response has already gone
// out, a reactor draining a queue — each acts FOR a tenant, and the callee has
// to know which one to write to the right books. Without this the only place
// left to put the org is the argument, and an org in the argument is an org the
// caller chose: any caller could then name any tenant and be believed.
//
//	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: org})
//	_, err := zip.Call[GrantIn, GrantOut](ctx, conn, "finance_grant", &in)
//
// An inbound request always wins. [forwardIdentity] prefers the gateway's
// assertion and reads this only when there is none, so this can supply an
// identity where none exists but can never override or launder one — which is
// what keeps [Ctx.Forward]'s guarantee intact. It is a statement by one of our
// own processes, trusted exactly as far as the socket's peer credential makes
// it trustworthy (see [Peer]), and never as far as a gateway's assertion.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, statedKey{}, c)
}

// Caller is the gateway's assertion about who a request is for, read as one
// value. It is what [Ctx.Forward] propagates and what [Call] carries.
//
// Empty fields mean the gateway said nothing — local dev, a direct ingress, or
// a call with no request behind it. Treat empty as unauthenticated, never as
// permitted.
// The two admin fields are separate authorities and reading one for the other
// is a privilege escalation: OrgAdmin says a person administers THEIR OWN org,
// Owner says which org that is. A deployment that reserves one org for platform
// operators gates its cross-tenant surfaces on Owner alone.
type Caller struct {
	Org       string
	Project   string
	User      string
	Name      string
	Email     string
	Owner     string
	Admin     bool
	OrgAdmin  bool
	RequestID string
}

// headers renders a stated caller onto the wire. Empty fields are omitted
// rather than sent blank, so "said nothing" and "said empty" stay the same
// thing on both sides of the call.
func (c Caller) headers() map[string]string {
	h := make(map[string]string, len(identityHeaders))
	for k, v := range map[string]string{
		HeaderOrg:       c.Org,
		HeaderProject:   c.Project,
		HeaderUser:      c.User,
		HeaderUserName:  c.Name,
		HeaderUserEmail: c.Email,
		HeaderUserOwner: c.Owner,
		HeaderRequestID: c.RequestID,
	} {
		if v != "" {
			h[k] = v
		}
	}
	if c.Admin {
		h[HeaderUserAdmin] = "true"
	}
	if c.OrgAdmin {
		h[HeaderUserOrgAdmin] = "true"
	}
	return h
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
// A context with no request behind it reads back whatever [WithCaller] stated
// on it, so what a background caller says it acts for is what the code running
// under that context sees — one value, written and read the same way, rather
// than a statement that only becomes visible one hop later.
func CallerOf(ctx context.Context) Caller {
	rc := requestOf(ctx)
	if rc == nil {
		stated, _ := ctx.Value(statedKey{}).(Caller)
		return stated
	}
	h := &rc.Request.Header
	return Caller{
		Org:       string(h.Peek(HeaderOrg)),
		Project:   string(h.Peek(HeaderProject)),
		User:      string(h.Peek(HeaderUser)),
		Name:      string(h.Peek(HeaderUserName)),
		Email:     string(h.Peek(HeaderUserEmail)),
		Owner:     string(h.Peek(HeaderUserOwner)),
		Admin:     string(h.Peek(HeaderUserAdmin)) == "true",
		OrgAdmin:  string(h.Peek(HeaderUserOrgAdmin)) == "true",
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
