package zip

import (
	"context"
	"strings"
	"testing"
)

// ONE ADDRESS, ONE REGISTRATION — and that registration both declares and answers.
//
// v1.24.2 shipped App.Shadow(), a scope whose registrations yielded their
// address so that an op could be DECLARED in one place and ANSWERED in another.
// It was removed in v1.24.3. The shape it accommodated is a braid: a
// declaration and the handler that satisfies it, written apart, held together
// by a framework verb. Unbraiding costs nothing, because a typed registration
// is ALREADY both halves — the handler the router installs and the op every
// projection reads — so the two can simply be the same line.
//
// These tests pin both directions: the braid is refused, the unbraided program
// is complete.

// braidedImpl answers the address.
func braidedImpl() *App {
	a := quiet("impl")
	a.Post("/v1/o11y/roles", func(c *Ctx) error { return c.String(200, "runtime") })
	return a
}

// braidedTable declares the same address somewhere else. Two claims.
func braidedTable() *App {
	t := quiet("table")
	Post(t.Group("/v1/o11y"), "/roles", func(_ context.Context, in *roleIn) (*roleOut, error) {
		return &roleOut{ID: in.Name}, nil
	}, WithOperationID("CreateRole"))
	return t
}

// TestDeclaringApartFromTheHandlerIsRefused: there is no scope, no option and
// no verb that makes two registrations at one address legal. The check has one
// behaviour and cannot be talked out of it.
func TestDeclaringApartFromTheHandlerIsRefused(t *testing.T) {
	host := quiet("host")
	host.Use(braidedImpl())
	host.Use(braidedTable())

	err := host.Build()
	if err == nil {
		t.Fatal("a declaration registered apart from its handler was accepted — " +
			"two claims at one address must stay a refused program")
	}
	// Both parties named, in the words the check has always used.
	for _, want := range []string{"POST /v1/o11y/roles", "declared by", "impl", "table"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// TestOneRegistrationDeclaresAndAnswers is the same intent, unbraided: the op
// reaches the document under the id its author wrote, AND it is what answers
// the address. Nothing to keep in sync, because there is only one thing.
func TestOneRegistrationDeclaresAndAnswers(t *testing.T) {
	svc := quiet("o11y")
	Post(svc.Group("/v1/o11y"), "/roles", func(_ context.Context, in *roleIn) (*roleOut, error) {
		return &roleOut{ID: in.Name}, nil
	}, WithOperationID("CreateRole"))

	host := quiet("host")
	host.Use(svc)
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	reg := host.Registry()
	if len(reg) != 1 {
		t.Fatalf("%d ops, want 1", len(reg))
	}
	if reg[0].OperationID != "CreateRole" || reg[0].Path != "/v1/o11y/roles" {
		t.Fatalf("op = %s at %s, want CreateRole at /v1/o11y/roles", reg[0].OperationID, reg[0].Path)
	}
	if status, body := postJSON(t, host, "/v1/o11y/roles", `{"name":"admin"}`); status != 200 || !strings.Contains(body, `"admin"`) {
		t.Fatalf("POST /v1/o11y/roles: status=%d body=%q — the declaration must also be what answers", status, body)
	}
}

// TestGroupIsTheOnlyScopeARegistrationNeeds: Use and Group between them place a
// definition's ops wherever a host wants them, and the address check still sees
// exactly one claim per address. This is the whole replacement for Shadow.
func TestGroupIsTheOnlyScopeARegistrationNeeds(t *testing.T) {
	host := quiet("host")
	host.Group("/v1").Use(service()) // service() declares CreateRole at /roles
	host.Get("/v1/health", func(c *Ctx) error { return c.String(200, "ok") })

	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	reg := host.Registry()
	if len(reg) != 1 || reg[0].OperationID != "CreateRole" || reg[0].Path != "/v1/roles" {
		t.Fatalf("registry = %+v, want CreateRole at /v1/roles", reg)
	}
}
