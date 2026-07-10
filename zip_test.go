package zip_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestBasicRouting hits the hello-world path through fiber.Test to
// confirm the Sinatra idiom + JSON response work end-to-end.
func TestBasicRouting(t *testing.T) {
	app := zip.New(zip.Config{AppName: "test", DisableStartupMessage: true})
	app.Get("/hello", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"message": "hi"})
	})

	req, _ := http.NewRequest("GET", "/hello", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json: %v / body=%s", err, body)
	}
	if got["message"] != "hi" {
		t.Fatalf("body=%s", body)
	}
}

// TestHTTPError checks zip.HTTPError → JSON error response.
func TestHTTPError(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/boom", func(c *zip.Ctx) error {
		return zip.ErrNotFound("nope")
	})
	req, _ := http.NewRequest("GET", "/boom", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "nope") {
		t.Fatalf("body=%s", body)
	}
}

// TestTyped exercises the generic typed handler + reflection-based
// validation + OpenAPI route installation.
func TestTyped(t *testing.T) {
	type In struct {
		Email string `json:"email" validate:"required,minlen=3"`
	}
	type Out struct {
		OK bool `json:"ok"`
	}
	app := zip.New(zip.Config{DisableStartupMessage: true})
	zip.Post(app, "/v1/test", func(ctx context.Context, in *In) (*Out, error) {
		return &Out{OK: true}, nil
	})

	// Valid call.
	req, _ := http.NewRequest("POST", "/v1/test",
		strings.NewReader(`{"email":"z@hanzo.ai"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body=%s", resp.StatusCode, body)
	}

	// Invalid call — missing email.
	req2, _ := http.NewRequest("POST", "/v1/test",
		strings.NewReader(`{}`))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := app.Fiber().Test(req2)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp2.StatusCode != 400 {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("status %d body=%s, want 400", resp2.StatusCode, body)
	}
}

// TestGroup verifies app.Group prefixing.
func TestGroup(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	v1 := app.Group("/v1")
	v1.Get("/ping", func(c *zip.Ctx) error {
		return c.String(200, "pong")
	})
	req, _ := http.NewRequest("GET", "/v1/ping", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// TestIdentityHeaders confirms c.Org/User/Email map to X-* headers.
func TestIdentityHeaders(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/who", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{
			"org":  c.Org(),
			"user": c.User(),
		})
	})
	req, _ := http.NewRequest("GET", "/who", nil)
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Id", "z")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"org":"hanzo"`) ||
		!strings.Contains(string(body), `"user":"z"`) {
		t.Fatalf("body=%s", body)
	}
}

// getBody drives one in-memory GET through the App and returns the body.
// It goes through app.Fiber().Test, which triggers finalize() — so the
// specificity sort has run and the route table is in most-specific-first order.
func getBody(t *testing.T, app *zip.App, path string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", path, nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%q): %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// TestSpecificity_StaticBeatsWildcardRegisteredFirst is the exact cloud
// order-int case, INVERTED: the wildcard is registered BEFORE the static route
// it would otherwise shadow. With registration-order matching this returns the
// wildcard; with specificity precedence the static route wins regardless.
func TestSpecificity_StaticBeatsWildcardRegisteredFirst(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	// Wildcard FIRST — no order-int, no manual sequencing.
	app.Get("/v1/iam/*", func(c *zip.Ctx) error { return c.String(200, "wild") })
	app.Get("/v1/iam/keys", func(c *zip.Ctx) error { return c.String(200, "keys") })

	if got := getBody(t, app, "/v1/iam/keys"); got != "keys" {
		t.Fatalf("GET /v1/iam/keys = %q, want \"keys\" (static must beat wildcard regardless of registration order)", got)
	}
	// The wildcard still catches everything else under the prefix.
	if got := getBody(t, app, "/v1/iam/anything"); got != "wild" {
		t.Fatalf("GET /v1/iam/anything = %q, want \"wild\"", got)
	}
}

// TestSpecificity_StaticThenParamThenWildcard proves the full precedence
// ladder on one prefix: static literal beats :param beats trailing *, with the
// routes registered in the LEAST-specific-first order to prove order is
// irrelevant.
func TestSpecificity_StaticThenParamThenWildcard(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/users/*", func(c *zip.Ctx) error { return c.String(200, "wild") })
	app.Get("/users/:id", func(c *zip.Ctx) error { return c.String(200, "param") })
	app.Get("/users/me", func(c *zip.Ctx) error { return c.String(200, "static") })

	if got := getBody(t, app, "/users/me"); got != "static" {
		t.Fatalf("GET /users/me = %q, want \"static\" (static beats :param and *)", got)
	}
	if got := getBody(t, app, "/users/42"); got != "param" {
		t.Fatalf("GET /users/42 = %q, want \"param\" (:param beats trailing *)", got)
	}
}

// TestSpecificity_DeeperStaticBeatsShallowWildcard proves a deeper static
// pattern wins over a shallower wildcard even though the wildcard would match.
func TestSpecificity_DeeperStaticBeatsShallowWildcard(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/a/*", func(c *zip.Ctx) error { return c.String(200, "wild") })
	app.Get("/a/b/c", func(c *zip.Ctx) error { return c.String(200, "deep") })

	if got := getBody(t, app, "/a/b/c"); got != "deep" {
		t.Fatalf("GET /a/b/c = %q, want \"deep\" (/a/b/c beats /a/*)", got)
	}
	if got := getBody(t, app, "/a/x"); got != "wild" {
		t.Fatalf("GET /a/x = %q, want \"wild\"", got)
	}
}

// TestSpecificity_DuplicateRoutePanics proves an exact (method, pattern)
// duplicate is a loud startup panic at finalize (ServeMux-style), naming the
// pattern — never a silent shadow.
func TestSpecificity_DuplicateRoutePanics(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/v1/dup", func(c *zip.Ctx) error { return c.String(200, "a") })
	app.Get("/v1/dup", func(c *zip.Ctx) error { return c.String(200, "b") })

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("duplicate (GET, /v1/dup) must panic at finalize")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "conflict") || !strings.Contains(msg, "/v1/dup") {
			t.Fatalf("panic = %q, want it to say \"conflict\" and name \"/v1/dup\"", msg)
		}
	}()
	_ = app.Fiber() // finalize — must panic here
}

// TestSpecificity_MiddlewareDeclarationOrder proves Use middleware is NOT
// reordered: it runs in declaration order and before the route handler. Only
// route precedence is sorted; middleware ordering is meaningful and preserved.
func TestSpecificity_MiddlewareDeclarationOrder(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	var order []string
	app.Use(func(c *zip.Ctx) error { order = append(order, "A"); return c.Continue() })
	app.Use(func(c *zip.Ctx) error { order = append(order, "B"); return c.Continue() })
	app.Get("/order", func(c *zip.Ctx) error { order = append(order, "H"); return c.String(200, "ok") })

	if got := getBody(t, app, "/order"); got != "ok" {
		t.Fatalf("GET /order = %q, want \"ok\"", got)
	}
	if len(order) != 3 || order[0] != "A" || order[1] != "B" || order[2] != "H" {
		t.Fatalf("execution order = %v, want [A B H] (Use keeps declaration order, before routes)", order)
	}
}

// TestSpecificity_RegisterAfterFinalizePanics proves the buffer is a
// startup-only concern: registering a route after finalize (Listen/Fiber())
// is a programming error and panics.
func TestSpecificity_RegisterAfterFinalizePanics(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/early", func(c *zip.Ctx) error { return c.String(200, "ok") })
	_ = app.Fiber() // finalize

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("registering a route after finalize must panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "before Listen") {
			t.Fatalf("panic = %q, want the register-before-Listen message", msg)
		}
	}()
	app.Get("/late", func(c *zip.Ctx) error { return c.String(200, "no") })
}
