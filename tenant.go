package zip

import (
	"context"
	"strings"
)

// The tenancy read, once, for every transport.
//
// "CallerOf(ctx).Org != empty" is NOT the check, and the reason is the whole of
// why this lives in zip rather than in each callee. X-Org-Id on its own is a
// client-settable header: a caller reaching a service directly — in-cluster, or
// on a public host whose edge has not yet been locked down — can present an org
// with NO validated user beside it. "The org is non-empty" is then a statement
// the caller made about itself, and the value is retained past the request as a
// store key and a ledger key.
//
// Where the principal COMES FROM is IAM's (HIP-0134): it is established once,
// nothing re-derives it, and a call carries it as delegation rather than
// assertion. Checking that one ARRIVED and is bounded before using it as a key
// is the callee's, and it is not a second implementation of IAM.

// MaxOrgLen bounds the org key. The org is the validated IAM owner claim — a
// short DNS-ish label — so anything longer is malformed or hostile and is
// refused before it can become a storage key or a namespace.
const MaxOrgLen = 128

// Tenant is the tenant a call may act for, or ("", false).
//
// It returns false — not an empty string a caller might use anyway — on each of
// three conditions, all of which are the same defect at different depths:
//
//	no validated User claim   the org that rode along is the caller's own assertion
//	an empty Org              there is no tenant to act for
//	an Org over MaxOrgLen     malformed or hostile, and it is about to be a key
//
// The org is returned VERBATIM apart from surrounding space: never lower-cased,
// never truncated. Folding collapses distinct owners ("acme", "ACME", a 32-char
// prefix) into one bucket, which is itself a cross-tenant break.
//
// A handler that touches per-tenant state calls this and refuses !ok. A handler
// that re-derives the rule is the sixth copy this function exists to delete.
func Tenant(ctx context.Context) (string, bool) {
	c := CallerOf(ctx)
	if strings.TrimSpace(c.User) == "" {
		return "", false
	}
	org := strings.TrimSpace(c.Org)
	if org == "" || len(org) > MaxOrgLen {
		return "", false
	}
	return strings.Clone(org), true
}

// Delegate is the context a background continuation runs under: the in-flight
// request's caller, with the org replaced.
//
// It is for the one case a request handler legitimately acts for another
// tenant — a platform operator's fan-out, a cross-org reconciliation — and it
// keeps the ACTING user, so the audit trail still names a person. Passing an
// org through an argument instead is the thing §6 forbids: an org in the
// argument is an org the caller chose.
//
// The returned context carries no request, so it outlives the handler: both
// transports reuse the underlying request for the next call on the connection,
// and a goroutine holding one reads another caller's headers.
func Delegate(c *Ctx, org string) context.Context {
	as := CallerOf(c.Forward())
	as.Org = strings.TrimSpace(org)
	return WithCaller(context.Background(), as)
}
