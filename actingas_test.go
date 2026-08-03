package zip

import (
	"context"
	"testing"
)

// The security property, stated as tests: an impersonation is derivable only
// from an authenticated identity, and is distinguishable from one at every hop.

// It cannot invent a caller. This is the laundering guard, unchanged.
func TestActingAs_RefusesWithNothingAuthenticated(t *testing.T) {
	_, err := ActingAs(context.Background(), "victim-org")
	if err == nil {
		t.Fatal("ActingAs minted a caller out of nothing — that is the laundering hole")
	}
	if CallerOf(context.Background()).Org != "" {
		t.Error("a refused ActingAs still altered the caller")
	}
}

// It re-points an authenticated one, and records who is acting.
func TestActingAs_CarriesBothWhoActsAndOnWhoseBehalf(t *testing.T) {
	admin := Caller{User: "u-admin", Org: "platform", Owner: "platform", Admin: true}
	ctx := WithCaller(context.Background(), admin)

	acting, err := ActingAs(ctx, "acme")
	if err != nil {
		t.Fatalf("ActingAs: %v", err)
	}
	got := CallerOf(acting)
	if got.Org != "acme" {
		t.Errorf("Org = %q, want acme — the call must reach the target's ledger", got.Org)
	}
	if got.ActedBy != "u-admin" {
		t.Errorf("ActedBy = %q, want u-admin — an audit row that omits the actor records the wrong one", got.ActedBy)
	}
	if got.User != "u-admin" {
		t.Errorf("User = %q; the acting principal must not be erased", got.User)
	}
	// The original context is untouched.
	if CallerOf(ctx).Org != "platform" {
		t.Error("ActingAs mutated the context it derived from")
	}
}

// A chain of hops cannot quietly reassign responsibility.
func TestActingAs_OriginalActorSurvivesAChain(t *testing.T) {
	ctx := WithCaller(context.Background(), Caller{User: "u-admin", Org: "platform"})
	one, err := ActingAs(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	two, err := ActingAs(one, "globex")
	if err != nil {
		t.Fatal(err)
	}
	got := CallerOf(two)
	if got.Org != "globex" {
		t.Errorf("Org = %q, want globex", got.Org)
	}
	if got.ActedBy != "u-admin" {
		t.Errorf("ActedBy = %q after two hops, want the ORIGINAL actor u-admin", got.ActedBy)
	}
}

// The impersonation travels, or the next hop sees a clean call from the target
// org and the audit trail ends at this process.
func TestActingAs_TravelsOnTheWire(t *testing.T) {
	ctx := WithCaller(context.Background(), Caller{User: "u-admin", Org: "platform"})
	acting, err := ActingAs(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	h := CallerOf(acting).headers()
	if h[HeaderOrg] != "acme" {
		t.Errorf("forwarded org = %q, want acme — this is the wrong-ledger bug", h[HeaderOrg])
	}
	if h[HeaderActedBy] != "u-admin" {
		t.Errorf("forwarded %s = %q, want u-admin", HeaderActedBy, h[HeaderActedBy])
	}
	var forwarded bool
	for _, name := range identityHeaders {
		if name == HeaderActedBy {
			forwarded = true
		}
	}
	if !forwarded {
		t.Error("X-Acted-By is not in the forwarded identity set, so the impersonation stops at this hop")
	}
}

// The project is scoped to the original org, so it must not ride along.
func TestActingAs_DoesNotCarryTheOriginalProject(t *testing.T) {
	ctx := WithCaller(context.Background(), Caller{User: "u", Org: "platform", Project: "p-platform"})
	acting, _ := ActingAs(ctx, "acme")
	if got := CallerOf(acting).Project; got != "" {
		t.Errorf("Project = %q; a project belongs to the org it was scoped to", got)
	}
}
