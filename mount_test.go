package zip_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// These tests pin the composition contract behind App.Mount: a foreign
// net/http.Handler mounts as an ordinary wildcard route via
//
//	app.All(prefix+"/*", zip.AdaptNetHTTP(h))
//
// which is exactly what the (deprecated) App.Mount does — so the two are
// proven behaviour-identical here. The load-bearing property is specificity:
// a static route registered AFTER the mount still wins, because the mounted
// subtree is a normal route subject to the zap-proto/fiber fork's
// most-specific-wins precedence — NOT a separate pre-router prefix dispatch.

// call issues an in-memory request through fiber.Test and returns
// (status, body). Empty body sends no request body.
func call(t *testing.T, app *zip.App, method, target, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, target, r)
	if err != nil {
		t.Fatalf("NewRequest(%s %s): %v", method, target, err)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%s %s): %v", method, target, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// echoHandler is a plain net/http handler that reports the path, query and
// body it received, so tests can prove the request reached it intact.
func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w,
			"net/http path="+r.URL.Path+" query="+r.URL.RawQuery+" body="+string(b))
	})
}

// TestMount_NewForm_Serves proves the recommended form —
// app.All(prefix+"/*", zip.AdaptNetHTTP(h)) — serves the mounted subtree.
func TestMount_NewForm_Serves(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.All("/api/*", zip.AdaptNetHTTP(echoHandler()))

	status, body := call(t, app, "GET", "/api/foo", "")
	if status != 200 {
		t.Fatalf("GET /api/foo: status %d, want 200 (body=%s)", status, body)
	}
	if !strings.Contains(body, "net/http path=/api/foo") {
		t.Fatalf("GET /api/foo: body=%q, want it served by the mounted net/http handler", body)
	}
}

// TestMount_StaticBeatsMount is the load-bearing proof: a static route
// registered AFTER the wildcard mount still wins for its exact path, while
// every other subpath falls through to the mount. This only holds because
// the mount is an ordinary route participating in specificity precedence —
// registration order is inverted on purpose.
func TestMount_StaticBeatsMount(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	// Wildcard mount FIRST, static route SECOND. Specificity, not order,
	// must decide.
	app.All("/v1/commerce/*", zip.AdaptNetHTTP(echoHandler()))
	app.Get("/v1/commerce/health", func(c *zip.Ctx) error {
		return c.String(200, "static-health")
	})

	// Exact static path: the later, more-specific static route wins.
	if status, body := call(t, app, "GET", "/v1/commerce/health", ""); status != 200 || body != "static-health" {
		t.Fatalf("GET /v1/commerce/health: status=%d body=%q, want 200 \"static-health\" (the mount must NOT shadow the later static route)", status, body)
	}
	// Any other subpath: the mount serves it.
	if status, body := call(t, app, "GET", "/v1/commerce/orders", ""); status != 200 || !strings.Contains(body, "net/http path=/v1/commerce/orders") {
		t.Fatalf("GET /v1/commerce/orders: status=%d body=%q, want the mount to serve it", status, body)
	}
}

// TestMount_BodyAndPathIntact proves the adapted handler receives the request
// body and the full subpath + query unchanged through the wildcard mount.
func TestMount_BodyAndPathIntact(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.All("/api/*", zip.AdaptNetHTTP(echoHandler()))

	status, body := call(t, app, "POST", "/api/echo/42?q=hi", "payload-bytes")
	if status != 200 {
		t.Fatalf("POST /api/echo/42?q=hi: status %d, want 200 (body=%s)", status, body)
	}
	for _, want := range []string{"path=/api/echo/42", "query=q=hi", "body=payload-bytes"} {
		if !strings.Contains(body, want) {
			t.Fatalf("POST /api/echo/42?q=hi: response %q missing %q — request did not reach the handler intact", body, want)
		}
	}
}

// TestMount_DeprecatedAliasIdentical proves the deprecated App.Mount is a
// behaviour-identical alias for the recommended form: it serves the subtree
// AND yields to a later, more-specific static route exactly as All+Adapt does.
func TestMount_DeprecatedAliasIdentical(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	app.Mount("/legacy", echoHandler()) // deprecated verb, still supported
	app.Get("/legacy/health", func(c *zip.Ctx) error {
		return c.String(200, "static-health")
	})

	// Subtree served by the mount.
	if status, body := call(t, app, "GET", "/legacy/foo", ""); status != 200 || !strings.Contains(body, "net/http path=/legacy/foo") {
		t.Fatalf("GET /legacy/foo via Mount: status=%d body=%q, want the mount to serve it", status, body)
	}
	// Later static route still wins — identical to app.All + AdaptNetHTTP.
	if status, body := call(t, app, "GET", "/legacy/health", ""); status != 200 || body != "static-health" {
		t.Fatalf("GET /legacy/health via Mount: status=%d body=%q, want static to win (Mount must be identical to All+AdaptNetHTTP)", status, body)
	}
}
