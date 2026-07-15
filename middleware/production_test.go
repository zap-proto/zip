package middleware_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// brandForHost is a test-local white-label resolver mirroring the shape of
// cloud.BrandForHostOK: a Host at or under a brand's registrable domain resolves
// to that brand id; an unknown Host resolves to "" so ProductionHeaders falls
// back to Neutral. Port is stripped and the compare is case-insensitive.
func brandForHost(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	for _, bd := range []struct{ brand, domain string }{
		{"hanzo", "hanzo.ai"},
		{"lux", "lux.network"},
		{"zoo", "zoo.ngo"},
	} {
		if host == bd.domain || strings.HasSuffix(host, "."+bd.domain) {
			return bd.brand
		}
	}
	return ""
}

// prodApp builds an app whose ServerHeader default is the leaky "zip" so the
// tests prove ProductionHeaders overrides it per request. RequestID +
// ProductionHeaders are registered together, as every service wires them.
func prodApp(cfg middleware.ProductionHeadersConfig) *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true, ServerHeader: "zip"})
	app.Use(middleware.RequestID(), middleware.ProductionHeaders(cfg))
	app.Get("/ok", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	app.Get("/boom", func(c *zip.Ctx) error { return zip.ErrForbidden("nope") })
	return app
}

func get(t *testing.T, app *zip.App, host, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+host+path, nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%s%s): %v", host, path, err)
	}
	return resp
}

// TestProductionHeaders_ServerBrandByHost is the white-label isolation contract:
// the Server header is the brand of the request Host, an unknown Host gets the
// neutral default, and NO Host ever leaks the framework name or a hardcoded
// single brand. A lux/zoo caller must never see "hanzo".
func TestProductionHeaders_ServerBrandByHost(t *testing.T) {
	app := prodApp(middleware.ProductionHeadersConfig{Brand: brandForHost, Neutral: "api"})
	cases := map[string]string{
		"api.hanzo.ai":    "hanzo",
		"hanzo.ai":        "hanzo",
		"api.lux.network": "lux",
		"lux.network":     "lux",
		"api.zoo.ngo":     "zoo",
		"console.zoo.ngo": "zoo",
		"api.unknown.dev": "api", // neutral — NOT hanzo, NOT a framework name
		"localhost":       "api",
	}
	for host, want := range cases {
		got := get(t, app, host, "/ok").Header.Get("Server")
		if got != want {
			t.Errorf("Host %q: Server=%q want %q", host, got, want)
		}
		for _, bad := range []string{"fasthttp", "fiber", "zip", "hanzoai"} {
			if strings.EqualFold(got, bad) {
				t.Errorf("Host %q: Server leaked framework/brand %q", host, got)
			}
		}
	}
	// Explicit white-label isolation: lux/zoo hosts NEVER resolve to hanzo.
	for _, host := range []string{"api.lux.network", "lux.network", "api.zoo.ngo", "console.zoo.ngo"} {
		if got := get(t, app, host, "/ok").Header.Get("Server"); got == "hanzo" {
			t.Errorf("Host %q leaked hanzo on a non-hanzo brand", host)
		}
	}
}

// TestProductionHeaders_RequestIDUniversal proves X-Request-Id rides out on the
// success, error, and 404 paths alike, and that an inbound id is echoed back
// (Stripe Request-Id / CF-Ray equivalent — the support-correlation header).
func TestProductionHeaders_RequestIDUniversal(t *testing.T) {
	app := prodApp(middleware.ProductionHeadersConfig{Brand: brandForHost, Neutral: "api"})
	for _, path := range []string{"/ok", "/boom", "/does-not-exist"} {
		resp := get(t, app, "api.hanzo.ai", path)
		if resp.Header.Get("X-Request-Id") == "" {
			t.Errorf("path %s (status %d): missing X-Request-Id on response", path, resp.StatusCode)
		}
	}
	// Inbound id is propagated to the response verbatim.
	req, _ := http.NewRequest("GET", "http://api.hanzo.ai/boom", nil)
	req.Header.Set("X-Request-Id", "rid-abc-123")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if got := resp.Header.Get("X-Request-Id"); got != "rid-abc-123" {
		t.Errorf("inbound X-Request-Id not echoed: got %q", got)
	}
}

// TestProductionHeaders_PostureOnErrorPath confirms the Server brand and the
// security floor hold on error and 404 responses — the posture is not a
// happy-path-only decoration.
func TestProductionHeaders_PostureOnErrorPath(t *testing.T) {
	app := prodApp(middleware.ProductionHeadersConfig{Brand: brandForHost, Neutral: "api", Version: "v1.2.3", HSTS: true})
	for _, path := range []string{"/boom", "/nope-404"} {
		resp := get(t, app, "api.lux.network", path)
		if got := resp.Header.Get("Server"); got != "lux" {
			t.Errorf("path %s (status %d): Server=%q want lux", path, resp.StatusCode, got)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("path %s: missing nosniff on error path", path)
		}
		if resp.Header.Get("X-Api-Version") != "v1.2.3" {
			t.Errorf("path %s: missing X-Api-Version on error path", path)
		}
	}
}

// TestProductionHeaders_VersionAndSecurityFloor checks the version signal and
// the HSTS + nosniff floor on the happy path, and that X-Powered-By is never
// emitted (the standard forbids it).
func TestProductionHeaders_VersionAndSecurityFloor(t *testing.T) {
	app := prodApp(middleware.ProductionHeadersConfig{Brand: brandForHost, Neutral: "api", Version: "v1.786.207", HSTS: true})
	resp := get(t, app, "api.hanzo.ai", "/ok")
	if got := resp.Header.Get("X-Api-Version"); got != "v1.786.207" {
		t.Errorf("X-Api-Version=%q want v1.786.207", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options=%q want nosniff", got)
	}
	if got := resp.Header.Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=63072000") || !strings.Contains(got, "includeSubDomains") {
		t.Errorf("Strict-Transport-Security=%q", got)
	}
	if got := resp.Header.Get("X-Powered-By"); got != "" {
		t.Errorf("leaked X-Powered-By=%q", got)
	}
}

// TestProductionHeaders_Defaults covers the neutral fallback and the
// nil-Brand / off-toggle behavior: empty Neutral becomes "api"; a nil Brand
// makes every response use Neutral (even a known-brand Host); HSTS/Version
// off omit their headers.
func TestProductionHeaders_Defaults(t *testing.T) {
	// Empty Neutral -> "api" for an unknown host.
	app := prodApp(middleware.ProductionHeadersConfig{Brand: brandForHost})
	if got := get(t, app, "api.unknown.dev", "/ok").Header.Get("Server"); got != "api" {
		t.Errorf("empty Neutral: Server=%q want api", got)
	}

	// nil Brand -> always Neutral, even for a would-be-hanzo Host.
	app2 := prodApp(middleware.ProductionHeadersConfig{Neutral: "lux"})
	if got := get(t, app2, "api.hanzo.ai", "/ok").Header.Get("Server"); got != "lux" {
		t.Errorf("nil Brand: Server=%q want lux (Neutral)", got)
	}

	// HSTS off + Version empty -> neither header emitted; nosniff still on.
	app3 := prodApp(middleware.ProductionHeadersConfig{Brand: brandForHost, Neutral: "api"})
	resp := get(t, app3, "api.hanzo.ai", "/ok")
	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS off but header present: %q", got)
	}
	if got := resp.Header.Get("X-Api-Version"); got != "" {
		t.Errorf("Version empty but header present: %q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff should always be set: %q", got)
	}
}
