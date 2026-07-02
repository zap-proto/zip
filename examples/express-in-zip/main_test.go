package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExpressInZip is the proof point: the legacy TS handler in app.ts,
// transpiled by real esbuild, loaded into real goja, mounted on real
// Fiber via zip, exercised over a real HTTP roundtrip.
func TestExpressInZip(t *testing.T) {
	app, err := setup()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("GET /legacy/foo", func(t *testing.T) {
		resp, err := app.Fiber().Test(httptest.NewRequest("GET", "/legacy/foo", nil))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		assertContainsAll(t, string(body), `"ok":true`, `"path":"/foo"`, `"body":null`)
	})

	t.Run("POST /legacy/bar echoes body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/legacy/bar", strings.NewReader(`{"x":1}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		assertContainsAll(t, string(body), `"ok":true`, `"path":"/bar"`, `"x":1`)
	})
}

// TestRuntimeRoute drives the unified runner over HTTP: the request body
// is the source, :lang selects the backend. js evaluates in real goja; an
// unregistered language is a 404 with a structured error.
func TestRuntimeRoute(t *testing.T) {
	app, err := setup()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("POST /runtime/js evaluates source", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/runtime/js", strings.NewReader("40+2"))
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		// goja Export() of an integer arithmetic result is int64; JSON
		// encodes it as the bare number 42.
		assertContainsAll(t, string(body), `"result":42`)
	})

	t.Run("POST /runtime/cobol is 404 unknown language", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/runtime/cobol", strings.NewReader("DISPLAY 'HI'."))
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 404 {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		assertContainsAll(t, string(body), `"error":"unknown language"`)
	})
}

func assertContainsAll(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Fatalf("body %q missing %q", body, w)
		}
	}
}
