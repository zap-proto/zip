package zip

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// wirePOST is wireGET's counterpart with a JSON body, which the typed op needs
// and the untyped implementation ignores.
func wirePOST(t *testing.T, a *App, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(`{"name":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// A declared SHADOW: the op is in the document, and the handler registered
// ahead of it is what answers.
//
// This is hanzoai/o11y's shape at one route. The service's implementation has
// always answered /v1/o11y/roles; the published table declares the same address
// so that the operation reaches the document, the MCP tool list, the CLI and
// every generated SDK. Both registrations are deliberate, the order between
// them IS the contract, and until [App.Shadow] existed the program was refused
// for a conflict its author had already thought about.

func implApp() *App {
	a := quiet("impl")
	a.Post("/v1/o11y/roles", func(c *Ctx) error { return c.String(200, "runtime") })
	return a
}

func tableApp() *App {
	t := quiet("table")
	Post(t.Shadow().Group("/v1/o11y"), "/roles", func(_ context.Context, in *roleIn) (*roleOut, error) {
		return &roleOut{ID: in.Name}, nil
	}, WithOperationID("CreateRole"))
	return t
}

func TestShadowDeclaresTheOpAndYieldsTheAddress(t *testing.T) {
	host := quiet("host")
	host.Use(implApp())  // the IMPLEMENTATION, first
	host.Use(tableApp()) // the DECLARATION, behind it
	if err := host.Build(); err != nil {
		t.Fatalf("a declared shadow was refused: %v", err)
	}

	// The op reached the document, under the id its author wrote.
	reg := host.Registry()
	if len(reg) != 1 || reg[0].OperationID != "CreateRole" || reg[0].Path != "/v1/o11y/roles" {
		t.Fatalf("registry = %+v, want one CreateRole at /v1/o11y/roles", reg)
	}

	// And the IMPLEMENTATION is what answers the address.
	code, body := wirePOST(t, host, "/v1/o11y/roles")
	if code != 200 || body != "runtime" {
		t.Errorf("POST /v1/o11y/roles answered %d %q, want 200 \"runtime\" — the declaration "+
			"must yield the address to the handler registered ahead of it", code, body)
	}
}

// The same table, mounted into a host that has NO implementation, ANSWERS.
// That is what lets one table be one table: shadowed where something else
// serves, serving where nothing does, with nothing to keep in sync.
func TestShadowAloneAtItsAddressAnswers(t *testing.T) {
	host := quiet("host")
	host.Use(tableApp())
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	code, body := wirePOST(t, host, "/v1/o11y/roles")
	if code != 200 || !strings.Contains(body, `"id"`) {
		t.Errorf("POST /v1/o11y/roles answered %d %q, want the op's own answer", code, body)
	}
}

// The accidental collision is STILL refused, in the same words. This is the
// half of the check that must not be weakened: two registrations that both mean
// to answer one address is the defect the check exists for, and neither of them
// said otherwise.
func TestTwoAnswerersAtOneAddressAreStillRefused(t *testing.T) {
	host := quiet("host")
	host.Use(implApp())
	host.Use(implApp())
	err := host.Build()
	if err == nil {
		t.Fatal("two unshadowed claims at one address must still be refused")
	}
	if !strings.Contains(err.Error(), "POST /v1/o11y/roles: declared by") {
		t.Errorf("refusal changed shape: %v", err)
	}
}

// A shadow registered FIRST is refused, because the router answers with the
// first registration: the declaration would stand in front of the handler it
// claims to yield to, and the handler would never be reached. This is what
// makes "order is the contract" a checked fact rather than a comment.
func TestShadowAheadOfItsHandlerIsRefused(t *testing.T) {
	host := quiet("host")
	host.Use(tableApp()) // the DECLARATION, wrongly first
	host.Use(implApp())
	err := host.Build()
	if err == nil {
		t.Fatal("a shadow registered ahead of its handler must be refused")
	}
	for _, want := range []string{"registered FIRST", "Order is the contract"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// Every claim yielding means nobody answers. Refused, and told what to drop.
func TestAllClaimsYieldingIsRefused(t *testing.T) {
	host := quiet("host")
	host.Use(tableApp())
	other := quiet("other")
	other.Shadow().Post("/v1/o11y/roles", func(c *Ctx) error { return c.String(200, "x") })
	host.Use(other)
	err := host.Build()
	if err == nil {
		t.Fatal("an address every registration yields must be refused")
	}
	if !strings.Contains(err.Error(), "none of them answers it") {
		t.Errorf("refusal does not say nobody answers: %v", err)
	}
}

// Shadow yields the ADDRESS to another handler, never the document slot to
// another op: a document holds one operation per method and path, so the second
// would be published nowhere at all.
func TestTwoTypedOpsAtOneAddressAreRefusedEvenShadowed(t *testing.T) {
	// The first op ANSWERS — rule one is satisfied — and the shadow behind it
	// still cannot have the document slot, which is the rule under test.
	first := quiet("first")
	Post(first.Group("/v1/o11y"), "/roles", func(_ context.Context, in *roleIn) (*roleOut, error) {
		return &roleOut{ID: in.Name}, nil
	}, WithOperationID("CreateRoleFirst"))

	host := quiet("host")
	host.Use(first)
	host.Use(tableApp())

	err := host.Build()
	if err == nil {
		t.Fatal("two typed ops at one address must be refused however they are marked")
	}
	if !strings.Contains(err.Error(), "two typed ops claim this address") {
		t.Errorf("refusal does not name the document slot: %v", err)
	}
	// And it is the ONLY complaint: rule one is satisfied, so the shadow is not
	// also accused of colliding.
	if strings.Contains(err.Error(), "declared by") {
		t.Errorf("rule one fired too: %v", err)
	}
}

// Shadow is a SCOPE, so a leaf registered on the returned group LATER is still
// shadowed — including one registered after the group was handed out, which is
// the property a decorator that only saw its own calls could not have.
func TestShadowIsAScopeAndReachesLateRegistrations(t *testing.T) {
	table := quiet("table")
	g := table.Shadow().Group("/v1/o11y")
	g.Post("/roles", func(c *Ctx) error { return c.String(200, "late") }) // registered later
	nested := g.Group("/sub")
	nested.Get("/thing", func(c *Ctx) error { return c.String(200, "nested") })

	host := quiet("host")
	host.Use(implApp())
	impl := quiet("impl2")
	impl.Get("/v1/o11y/sub/thing", func(c *Ctx) error { return c.String(200, "runtime2") })
	host.Use(impl)
	host.Use(table)

	if err := host.Build(); err != nil {
		t.Fatalf("a late registration in a shadowed scope was refused: %v", err)
	}
	if code, body := wireGET(t, host, "/v1/o11y/sub/thing"); code != 200 || body != "runtime2" {
		t.Errorf("nested shadow answered %d %q, want the implementation — the scope must "+
			"ride down every level of nesting", code, body)
	}
}

// The other deliberate shape: a second UNTYPED handler at an address, declared.
// This is hanzoai/cloud's openapi generator test — it pins that a duplicate
// registration and a middleware chain are indistinguishable to the router, one
// entry with two chained handlers — and the registration must stay expressible
// for that fact to be pinnable at all.
//
// Nothing about the wire changes: the shadow is installed exactly as it always
// was, fiber chains it behind the first handler, and the first is what answers.
func TestShadowedUntypedRegistrationChainsBehindTheFirst(t *testing.T) {
	dup := quiet("dup")
	dup.Get("/v1/bots", func(c *Ctx) error { return c.String(200, "machine") })
	dup.Shadow().Get("/v1/bots", func(c *Ctx) error { return c.String(200, "run") })
	if err := dup.Build(); err != nil {
		t.Fatalf("a declared double registration was refused: %v", err)
	}

	// ONE route entry, TWO chained handlers — the fact cloud's generator pins.
	var routes int
	var handlers int
	for _, r := range dup.Fiber().GetRoutes(true) {
		if r.Path == "/v1/bots" && r.Method == "GET" {
			routes++
			handlers = len(r.Handlers)
		}
	}
	if routes != 1 || handlers != 2 {
		t.Errorf("GET /v1/bots is %d entries with %d handlers, want 1 entry with 2", routes, handlers)
	}
	// And the FIRST registration answers.
	if code, body := wireGET(t, dup, "/v1/bots"); code != 200 || body != "machine" {
		t.Errorf("answered %d %q, want 200 \"machine\"", code, body)
	}
}

// Shadow is orthogonal to the middleware seam: a shadowed group is a group, so
// its own Use still wraps the routes beneath it. Declaring that a scope yields
// its addresses says nothing about what runs when one of them IS reached, and
// dropping the chain would be the same silent hole OpTarget's doc warns about.
func TestShadowIsOrthogonalToTheMiddlewareSeam(t *testing.T) {
	var ran bool
	table := quiet("table")
	g := table.Shadow().Group("/v1/o11y")
	g.Use(H(func(c *Ctx) error { ran = true; return c.Continue() }))
	g.Get("/only", func(c *Ctx) error { return c.String(200, "served") })

	host := quiet("host")
	host.Use(table)
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	if code, body := wireGET(t, host, "/v1/o11y/only"); code != 200 || body != "served" {
		t.Fatalf("answered %d %q", code, body)
	}
	if !ran {
		t.Error("the group's middleware did not run — Shadow ate the scope's chain")
	}
}
