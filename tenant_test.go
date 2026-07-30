package zip_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The naive rule — "the org is non-empty" — admits exactly the forge this
// function exists to refuse: an org with no validated user beside it is a
// statement the caller made about itself, and it is about to become a store key.
func TestTenantRefusesTheThreeWaysAnOrgIsNotOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    zip.Caller
	}{
		{"no caller at all", zip.Caller{}},
		{"an org with no validated user", zip.Caller{Org: "victim"}},
		{"a user with no org", zip.Caller{User: "u_1"}},
		{"a blank org", zip.Caller{User: "u_1", Org: "   "}},
		{"an org past the bound", zip.Caller{User: "u_1", Org: strings.Repeat("a", zip.MaxOrgLen+1)}},
	} {
		ctx := zip.WithCaller(context.Background(), tc.c)
		if org, ok := zip.Tenant(ctx); ok {
			t.Fatalf("%s: admitted %q", tc.name, org)
		}
	}
}

// Verbatim: trimmed, never folded. "acme", "ACME" and a truncated prefix are
// three distinct owners, and collapsing them is itself a cross-tenant break.
func TestTenantIsVerbatim(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{User: "u_1", Org: "  ACME-Prod  "})
	org, ok := zip.Tenant(ctx)
	if !ok || org != "ACME-Prod" {
		t.Fatalf("Tenant = %q, %v — want ACME-Prod, true", org, ok)
	}
	at := zip.Caller{User: "u_1", Org: strings.Repeat("a", zip.MaxOrgLen)}
	if org, ok := zip.Tenant(zip.WithCaller(context.Background(), at)); !ok || len(org) != zip.MaxOrgLen {
		t.Fatalf("the bound is inclusive: got %d, %v", len(org), ok)
	}
}

// Delegate keeps the ACTING user, so the audit trail still names a person, and
// detaches from the request, so the continuation can outlive the handler.
func TestDelegateKeepsTheActorAndSurvivesTheRequest(t *testing.T) {
	app := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	c := app.TestCtx("GET", "/x")
	c.Fiber().Request().Header.Set(zip.HeaderUser, "u_admin")
	c.Fiber().Request().Header.Set(zip.HeaderOrg, "admin")

	ctx := zip.Delegate(c, "acme")
	org, ok := zip.Tenant(ctx)
	if !ok || org != "acme" {
		t.Fatalf("Tenant = %q, %v — want acme, true", org, ok)
	}
	if got := zip.CallerOf(ctx).User; got != "u_admin" {
		t.Fatalf("acting user = %q, want u_admin", got)
	}
	// Reset the request as a reused connection would: the delegated context
	// must not read whatever landed next.
	c.Fiber().Request().Header.Reset()
	if org, ok := zip.Tenant(ctx); !ok || org != "acme" {
		t.Fatalf("after the request was reused: %q, %v — the context borrowed it", org, ok)
	}
}
