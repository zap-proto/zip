package zip_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// TestRouteChainOrder pins the ONE registration order: middleware first, the
// terminal handler LAST, executed in exactly that order. A regression here
// inverts every auth gate in every app built on zip.
func TestRouteChainOrder(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	var order []string
	app.Get("/x",
		func(c *zip.Ctx) error { order = append(order, "mw1"); return c.Next() },
		func(c *zip.Ctx) error { order = append(order, "mw2"); return c.Next() },
		func(c *zip.Ctx) error { order = append(order, "handler"); return c.NoContent(204) },
	)
	if _, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/x", nil)); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(order) != 3 || order[0] != "mw1" || order[1] != "mw2" || order[2] != "handler" {
		t.Fatalf("chain order = %v, want [mw1 mw2 handler]", order)
	}
}

// TestRouteChainGateStops pins the abort contract: a middleware that renders
// WITHOUT calling Next stops the chain — the terminal never runs.
func TestRouteChainGateStops(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	reached := false
	app.Get("/x",
		func(c *zip.Ctx) error { return c.NoContent(401) }, // gate: no Next
		func(c *zip.Ctx) error { reached = true; return c.NoContent(200) },
	)
	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != 401 || reached {
		t.Fatalf("status=%d reached=%v, want 401,false", resp.StatusCode, reached)
	}
}
