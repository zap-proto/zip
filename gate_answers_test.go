package zip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A gate that cannot refuse is not a gate. Middleware in Use position may write a
// response and return without Next — an auth check, a rate limiter, a cache hit, a
// maintenance refusal. This is asserted rather than described because it was
// DESCRIBED as forbidden for a while ("a Handler may never terminate a request"),
// and a consumer read that and routed a 503 refusal through the host's error handler
// instead of answering, which a host that flattens errors to 500 then swallowed.
//
// What stays refused is a handler that CLAIMS an address from Use position — see
// TestUse_RefusesTerminal. The two are different facts and only one is a rule.
func TestUse_MiddlewareMayAnswer(t *testing.T) {
	app := New(Config{DisableStartupMessage: true})
	app.Use(H(func(c *Ctx) error {
		if c.Header("X-Token") == "" {
			c.SetHeader("Retry-After", "30")
			return c.JSON(http.StatusUnauthorized, map[string]any{"error": "no token"})
		}
		return c.Next()
	}))
	app.Get("/thing", func(c *Ctx) error { return c.String(200, "served") })
	if err := app.Build(); err != nil {
		t.Fatalf("Build refused a gate that answers: %v", err)
	}

	refused, err := app.Test(httptest.NewRequest(http.MethodGet, "/thing", nil))
	if err != nil {
		t.Fatal(err)
	}
	if refused.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401 — the gate could not short-circuit", refused.StatusCode)
	}
	if got := refused.Header.Get("Retry-After"); got != "30" {
		t.Errorf("no token: Retry-After = %q, want the gate's own header", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/thing", nil)
	req.Header.Set("X-Token", "t")
	allowed, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if allowed.StatusCode != http.StatusOK {
		t.Errorf("with token: status = %d, want 200 — Next must still reach the route", allowed.StatusCode)
	}
}
