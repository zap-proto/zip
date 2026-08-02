package zip

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// The walk says what a program MEANS. These say what it DOES — the same
// programs, driven over the wire, because a projection that is right on paper
// and wrong at the socket is wrong.

func wireGET(t *testing.T, a *App, path string, headers ...string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// guarded is a definition whose entire authentication seam is a pathless
// app.Use — the shape that made route-copying unsafe and that [App.Graft]'s
// delegation existed to protect.
func guarded(name string) *App {
	a := quiet(name)
	a.Use(H(func(c *Ctx) error {
		if c.Header("Authorization") == "" {
			return Errorf(401, "no credentials")
		}
		return c.Continue()
	}))
	a.Get("/thing", func(c *Ctx) error { return c.String(200, name+" thing") })
	return a
}

// TestWire_IncludedMiddlewareStaysInsideItsDefinition is the property the whole
// composition model is judged on, and inclusion by reference has to earn it the
// hard way: there is no delegation boundary at run time to hide behind, so the
// definition's middleware has to be composed onto the definition's OWN routes
// and nothing else.
//
// The failure this prevents is not hypothetical: a child's pathless Use, placed
// on the host's router, gates every route in the host binary.
func TestWire_IncludedMiddlewareStaysInsideItsDefinition(t *testing.T) {
	root := quiet("host")
	root.Get("/before", func(c *Ctx) error { return c.String(200, "before") })
	root.Use(guarded("iam"))
	root.Get("/after", func(c *Ctx) error { return c.String(200, "after") })

	// The guard DID come across: the definition's own route is gated.
	if code, _ := wireGET(t, root, "/thing"); code != 401 {
		t.Errorf("the definition's route answered %d without credentials — its guard was dropped", code)
	}
	if code, b := wireGET(t, root, "/thing", "Authorization", "yes"); code != 200 || b != "iam thing" {
		t.Errorf("the definition's route with credentials: %d %q", code, b)
	}
	// And it did NOT bleed — in EITHER direction. Routes written before the
	// inclusion and routes written after it are both untouched.
	if code, b := wireGET(t, root, "/before"); code != 200 || b != "before" {
		t.Errorf("host route written BEFORE the inclusion answered %d %q — the guard bled backwards", code, b)
	}
	if code, b := wireGET(t, root, "/after"); code != 200 || b != "after" {
		t.Errorf("host route written AFTER the inclusion answered %d %q — the guard bled forwards", code, b)
	}
}

// TestWire_GroupIsLexicalScope: middleware declared in a group applies to that
// group and to nothing else, including nothing that comes after it.
func TestWire_GroupIsLexicalScope(t *testing.T) {
	root := quiet("host")
	v1 := root.Group("/v1")
	v1.Use(H(func(c *Ctx) error {
		if c.Header("X-Key") == "" {
			return Errorf(401, "no key")
		}
		return c.Continue()
	}))
	v1.Get("/x", func(c *Ctx) error { return c.String(200, "v1 x") })
	root.Get("/open", func(c *Ctx) error { return c.String(200, "open") })

	if code, _ := wireGET(t, root, "/v1/x"); code != 401 {
		t.Errorf("group route answered %d without the key — the group's Use did not apply", code)
	}
	if code, b := wireGET(t, root, "/v1/x", "X-Key", "k"); code != 200 || b != "v1 x" {
		t.Errorf("group route with the key: %d %q", code, b)
	}
	if code, b := wireGET(t, root, "/open"); code != 200 || b != "open" {
		t.Errorf("a route outside the group answered %d %q — the group's Use escaped its scope", code, b)
	}
}

// TestWire_DiamondServesBothOccurrences: one definition, two prefixes, and BOTH
// answer. The router keys on the occurrence, so two inclusions are two route
// sets, not one that the second inclusion silently lost.
func TestWire_DiamondServesBothOccurrences(t *testing.T) {
	billing := billingApp()
	root := quiet("cloud")
	root.Group("/v1").Use(billing)
	root.Group("/admin").Use(billing)

	for _, p := range []string{"/v1/invoices/i-1", "/admin/invoices/i-1"} {
		code, body := wireGET(t, root, p)
		if code != 200 || !strings.Contains(body, "i-1") {
			t.Errorf("%s answered %d %q", p, code, body)
		}
	}
}

// TestWire_DifferentGuardsPerOccurrence. The same definition legitimately runs
// under one guard in one place and another guard elsewhere; that is the
// semantics of an occurrence, not an approximation of it.
func TestWire_DifferentGuardsPerOccurrence(t *testing.T) {
	shared := quiet("shared")
	shared.Get("/who", func(c *Ctx) error { return c.String(200, "shared") })

	need := func(h string) Handler {
		return func(c *Ctx) error {
			if c.Header(h) == "" {
				return Errorf(401, "want %s", h)
			}
			return c.Continue()
		}
	}
	root := quiet("host")
	pub := root.Group("/pub")
	pub.Use(need("X-User"))
	pub.Use(shared)
	adm := root.Group("/adm")
	adm.Use(need("X-Admin"))
	adm.Use(shared)

	if code, _ := wireGET(t, root, "/pub/who", "X-User", "u"); code != 200 {
		t.Errorf("/pub/who with a user credential answered %d", code)
	}
	if code, _ := wireGET(t, root, "/pub/who", "X-Admin", "a"); code != 401 {
		t.Errorf("/pub/who accepted the ADMIN credential (%d) — the occurrences share a guard", code)
	}
	if code, _ := wireGET(t, root, "/adm/who", "X-Admin", "a"); code != 200 {
		t.Errorf("/adm/who with an admin credential answered %d", code)
	}
	if code, _ := wireGET(t, root, "/adm/who", "X-User", "u"); code != 401 {
		t.Errorf("/adm/who accepted the USER credential (%d) — the occurrences share a guard", code)
	}
}

// TestWire_UndeclaredPathIsNotSwallowed. Inclusion registers exactly the
// addresses the definition declares, so a path nobody declares is a 404 from
// the host rather than being swallowed by a wildcard.
func TestWire_UndeclaredPathIsNotSwallowed(t *testing.T) {
	root := quiet("host")
	root.Group("/v1").Use(billingApp())
	if code, _ := wireGET(t, root, "/v1/nobody-declares-this"); code != 404 {
		t.Errorf("undeclared path answered %d, want 404 — the inclusion is swallowing", code)
	}
}

// TestWire_OneCtxAcrossAnInclusionBoundary. A definition's middleware and the
// host's middleware run in one request, so they must be handed ONE *Ctx —
// enrichment written by either has to be visible to the other. Binding a
// handler to the definition that declared it instead of the app that serves it
// breaks exactly this.
func TestWire_OneCtxAcrossAnInclusionBoundary(t *testing.T) {
	var seen []*Ctx
	inner := quiet("inner")
	inner.Use(H(func(c *Ctx) error { seen = append(seen, c); return c.Continue() }))
	inner.Get("/deep", func(c *Ctx) error {
		seen = append(seen, c)
		return c.NoContent(204)
	})

	root := quiet("host")
	root.Use(H(func(c *Ctx) error { seen = append(seen, c); return c.Continue() }))
	root.Group("/v1").Use(inner)

	if code, _ := wireGET(t, root, "/v1/deep"); code != 204 {
		t.Fatalf("/v1/deep answered %d", code)
	}
	if len(seen) != 3 {
		t.Fatalf("ran %d handlers, want 3", len(seen))
	}
	for i, c := range seen {
		if c != seen[0] {
			t.Fatalf("handler %d got a different *Ctx — handlers were bound to their definition, not to the server", i)
		}
	}
}

// TestWire_IncludedControlPlaneIsNotAdopted. zip's own projections belong to
// the process serving them. Inclusion reads a definition's whole program, so
// without an explicit rule a definition that had ever rendered its own document
// would hand the host ITS document at the host's well-known path — and the
// composition would publish the part as if it were the whole.
func TestWire_IncludedControlPlaneIsNotAdopted(t *testing.T) {
	child := billingApp()
	child.Prepare() // the definition renders its own control plane first

	host := quiet("host")
	Get(host, "/own", func(context.Context, *invoiceIn) (*invoiceOut, error) { return &invoiceOut{}, nil },
		WithOperationID("hostOwn"))
	host.Group("/v1").Use(child)
	host.Prepare()

	code, body := wireGET(t, host, SpecPath)
	if code != 200 {
		t.Fatalf("%s answered %d", SpecPath, code)
	}
	if !strings.Contains(body, `"title":"host"`) {
		t.Errorf("the included definition took the host's document: %s", firstN(body, 200))
	}
	if !strings.Contains(body, "/v1/invoices/{id}") {
		t.Errorf("the host's served document is missing the included op: %s", firstN(body, 400))
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
