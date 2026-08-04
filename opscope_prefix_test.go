package zip

import (
	"context"
	"testing"
)

type prefixIn struct{}
type prefixOut struct{ OK bool }

func prefixHandler(context.Context, *prefixIn) (*prefixOut, error) { return &prefixOut{true}, nil }

// TestTypedOpOnGroupIsServedAtTheComposedAddress pins the guarantee that matters
// to every consumer: whatever address the router serves, the declaration names.
//
// The two are computed by DIFFERENT code paths — materialise walks the
// composition tree, composeOps recomputes each op's absolute path with o.abs —
// and nothing in the type system forces them to agree. This is the assertion that
// does.
func TestTypedOpOnGroupIsServedAtTheComposedAddress(t *testing.T) {
	app := New(Config{DisableStartupMessage: true})
	billing := app.Group("/v1").Group("/billing")
	Post(billing, "/deposit", prefixHandler)

	var patterns []string
	for _, r := range app.Declaration().Routes {
		if r.Op != "" {
			patterns = append(patterns, r.Method+" "+r.Pattern)
		}
	}
	want := "POST /v1/billing/deposit"
	for _, p := range patterns {
		if p == want {
			return
		}
	}
	t.Fatalf("typed op routes = %v, want one to be %q", patterns, want)
}

// TestOpScopePrefixIsNotAnAbsoluteAddress documents WHY a decorator cannot ask a
// Router "what is your prefix?" and get a URL back — the question has no answer
// at registration time.
//
// zip's model is composition: the same definition can be included at more than
// one site, so a group has no single absolute prefix until a build resolves the
// tree. OpScope is a REGISTRATION-time value, so its Prefix is the local join
// only; the absolute address is recomputed per occurrence at compose time
// (composeOps → o.abs).
//
// This is the fact that broke hanzoai/commerce's groupPrefix, which read
// r.Fiber().(*fiber.Group).Prefix. That worked only because fiber flattened a
// group into one prefix at declaration — an approximation that silently becomes
// wrong the moment a definition is composed twice. A consumer that needs the
// address a route is SERVED at must read it from the declaration, after
// composition, not from the router it registered on.
func TestOpScopePrefixIsNotAnAbsoluteAddress(t *testing.T) {
	app := New(Config{DisableStartupMessage: true})
	if got := app.OpScope().Prefix; got != "" {
		t.Errorf("root scope prefix = %q, want empty", got)
	}

	// A group knows the leaf it was declared with, not where it will be served.
	billing := app.Group("/v1").Group("/billing")
	Post(billing, "/deposit", prefixHandler)

	// …and the served address is nonetheless correct, because composition — not
	// the scope — is what resolves it.
	found := false
	for _, r := range app.Declaration().Routes {
		if r.Pattern == "/v1/billing/deposit" {
			found = true
		}
	}
	if !found {
		t.Fatal("the composed route should be served at /v1/billing/deposit")
	}
}
