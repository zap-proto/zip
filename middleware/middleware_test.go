package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// ACCESS-CONTROL-ALLOW-ORIGIN NAMES EXACTLY ONE ORIGIN. An allowlist is checked
// against the request's Origin and the match is echoed; joining the list into
// one header is a value a browser rejects, and it publishes the whole allowlist
// to every caller besides.
func TestCORS_EchoesTheMatchingOrigin(t *testing.T) {
	h := middleware.CORS(middleware.CORSConfig{
		AllowOrigins: []string{"http://a.test", "http://b.test"},
	})
	for _, c := range []struct{ send, want, vary string }{
		{"http://a.test", "http://a.test", "Origin"},
		{"http://b.test", "http://b.test", "Origin"},
		{"http://evil.test", "", ""}, // unlisted: no header beats a wrong one
	} {
		resp := ride(t, h, c.send)
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != c.want {
			t.Errorf("Origin %q -> allow-origin %q, want %q", c.send, got, c.want)
		}
		if got := resp.Header.Get("Vary"); got != c.vary {
			t.Errorf("Origin %q -> Vary %q, want %q", c.send, got, c.vary)
		}
	}
}

// The wildcard stays the wildcard — but not beside credentials, where the spec
// forbids "*" and the echo is the only answer a browser will accept.
func TestCORS_WildcardAndCredentials(t *testing.T) {
	open := ride(t, middleware.CORS(middleware.CORSConfig{}), "http://x.test")
	if got := open.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("default allow-origin %q, want *", got)
	}
	creds := ride(t, middleware.CORS(middleware.CORSConfig{AllowCreds: true}), "http://x.test")
	if got := creds.Header.Get("Access-Control-Allow-Origin"); got != "http://x.test" {
		t.Errorf("with credentials allow-origin %q, want the echoed origin", got)
	}
}

// Max-Age is a COUNT OF SECONDS. It was rendered through time.Duration, whose
// String() reads nanoseconds — 3600 went out as "3.6µs", which is not a number
// and caches nothing.
func TestCORS_MaxAgeIsSeconds(t *testing.T) {
	resp := ride(t, middleware.CORS(middleware.CORSConfig{MaxAge: 3600}), "http://x.test")
	if got := resp.Header.Get("Access-Control-Max-Age"); got != "3600" {
		t.Errorf("max-age %q, want 3600", got)
	}
}

// ride drives one GET through h and returns the response.
func ride(t *testing.T, h zip.Handler, origin string) *http.Response {
	t.Helper()
	a := zip.New(zip.Config{AppName: "cors", DisableStartupMessage: true})
	a.Use(h)
	a.Get("/x", func(c *zip.Ctx) error { return c.String(http.StatusOK, "ok") })
	if err := a.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", origin)
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET /x: %v", err)
	}
	return resp
}
