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

// TestRuntimeRoute drives the unified runner over HTTP: :lang selects the
// backend and the body carries the source. js evaluates in real goja; an
// unregistered language is a 404 with a structured error.
//
// The body is the op's In as JSON, because the route is a typed op — which is
// what puts "run this source" in the document and in the MCP tool list. A raw
// body would have meant no schema, and no schema means no projection.
func TestRuntimeRoute(t *testing.T) {
	app, err := setup()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("POST /runtime/js evaluates source", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/runtime/js", strings.NewReader(`{"source":"40+2"}`))
		req.Header.Set("Content-Type", "application/json")
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
		req := httptest.NewRequest("POST", "/runtime/cobol", strings.NewReader(`{"source":"DISPLAY 'HI'."}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 404 {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("Content-Type = %q, want the RFC 9457 media type", ct)
		}
		body, _ := io.ReadAll(resp.Body)
		assertContainsAll(t, string(body), `"detail":"unknown language"`, `"status":404`)
	})

	// The op is in the document because it IS an op — the assertion that says
	// step 5 of setup() achieved something the fiber-level registration did not.
	t.Run("the runtime op is projected", func(t *testing.T) {
		spec := app.OpenAPISpec()
		paths, _ := spec["paths"].(map[string]map[string]any)
		if _, ok := paths["/runtime/{lang}"]; !ok {
			t.Fatalf("the runtime op is missing from the document; paths = %v", paths)
		}
		if len(app.MCPTools()) != 1 {
			t.Fatalf("tools = %v, want the one op", app.MCPTools())
		}
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
