package zip_test

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// These tests pin zip.Static: it serves files from any fs.FS (embed.FS AND
// os.DirFS are exercised), rejects traversal fail-closed, and — the load-
// bearing property — a missing file yields c.Next() so a later route or SPA
// catch-all still wins, while a more-specific route beats the wildcard by
// specificity, never by registration order.

//go:embed testdata/assets
var assetsEmbed embed.FS

func embedAssets(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(assetsEmbed, "testdata/assets")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	return sub
}

func dirAssets() fs.FS { return os.DirFS("testdata/assets") }

// serve issues an in-memory request (optionally with headers) and returns the
// full response plus the body it read.
func serve(t *testing.T, app *zip.App, method, target string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("NewRequest(%s %s): %v", method, target, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%s %s): %v", method, target, err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

// bothFS runs fn against Static backed by an embed.FS and an os.DirFS, proving
// the handler is fs.FS-generic.
func bothFS(t *testing.T, fn func(t *testing.T, assets fs.FS)) {
	t.Helper()
	t.Run("embed.FS", func(t *testing.T) { fn(t, embedAssets(t)) })
	t.Run("os.DirFS", func(t *testing.T) { fn(t, dirAssets()) })
}

func TestStatic_ServesFile(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		app := zip.New(zip.Config{DisableStartupMessage: true})
		app.Get("/assets/*", zip.Static(assets))

		want, _ := fs.ReadFile(assets, "main.css")
		resp, body := serve(t, app, "GET", "/assets/main.css", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		if body != string(want) {
			t.Fatalf("body %q, want %q", body, want)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Fatalf("Content-Type %q, want text/css…", ct)
		}
		if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(want)) {
			t.Fatalf("Content-Length %q, want %d", cl, len(want))
		}
	})
}

func TestStatic_Head(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		app := zip.New(zip.Config{DisableStartupMessage: true})
		app.Get("/assets/*", zip.Static(assets))

		want, _ := fs.ReadFile(assets, "main.css")
		resp, body := serve(t, app, "HEAD", "/assets/main.css", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		if body != "" {
			t.Fatalf("HEAD body %q, want empty", body)
		}
		if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(want)) {
			t.Fatalf("HEAD Content-Length %q, want %d (must report the size GET would send)", cl, len(want))
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Fatalf("HEAD Content-Type %q, want text/css…", ct)
		}
	})
}

// TestStatic_MissingFallsThroughToNext is the load-bearing behaviour: a missing
// file must NOT 500 or hard-404 — it calls c.Next() so a later route wins. Here
// a SPA catch-all registered after the wildcard serves the miss.
func TestStatic_MissingFallsThroughToNext(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		app := zip.New(zip.Config{DisableStartupMessage: true})
		app.Get("/assets/*", zip.Static(assets))
		app.Get("/*", func(c *zip.Ctx) error { return c.String(200, "spa") })

		resp, body := serve(t, app, "GET", "/assets/does-not-exist.css", nil)
		if resp.StatusCode != 200 || body != "spa" {
			t.Fatalf("missing file: status=%d body=%q, want 200 \"spa\" (Static must Next() to the catch-all)", resp.StatusCode, body)
		}
		// The real file still serves — Static didn't just always-Next.
		if resp, body := serve(t, app, "GET", "/assets/main.css", nil); resp.StatusCode != 200 || strings.Contains(body, "spa") {
			t.Fatalf("real file: status=%d body=%q, want the file, not the SPA", resp.StatusCode, body)
		}
	})
}

// With no later route, a missing file is a clean 404 (Next() with c.matched
// set), never a 500.
func TestStatic_Missing404WhenNoCatchAll(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/assets/*", zip.Static(embedAssets(t)))

	resp, _ := serve(t, app, "GET", "/assets/nope.css", nil)
	if resp.StatusCode != 404 {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
}

// TestStatic_TraversalBlocked proves no request can escape fsys to read the
// sibling secret, across several traversal encodings. os.DirFS is rooted at
// assets and Static's fs.ValidPath gate rejects "..", so the parent's
// secret.txt is unreachable — the response never carries its content and is
// never a 200.
func TestStatic_TraversalBlocked(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/assets/*", zip.Static(dirAssets()))

	for _, target := range []string{
		"/assets/../secret.txt",
		"/assets/../../secret.txt",
		"/assets/%2e%2e/secret.txt",
		"/assets/..%2fsecret.txt",
		"/assets/sub/../../secret.txt",
	} {
		resp, body := serve(t, app, "GET", target, nil)
		if strings.Contains(body, "TOP-SECRET") {
			t.Fatalf("%s LEAKED the sibling secret: %q", target, body)
		}
		if resp.StatusCode == 200 {
			t.Fatalf("%s returned 200 (%q); traversal must fail closed", target, body)
		}
	}
}

// TestStatic_LaterStaticRouteWins: a specific route registered AFTER the
// wildcard Static still wins for its exact path (specificity, not order),
// while every other subpath is served by Static.
func TestStatic_LaterStaticRouteWins(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/assets/*", zip.Static(embedAssets(t)))
	app.Get("/assets/special.txt", func(c *zip.Ctx) error { return c.String(200, "special-route") })

	if resp, body := serve(t, app, "GET", "/assets/special.txt", nil); resp.StatusCode != 200 || body != "special-route" {
		t.Fatalf("/assets/special.txt: status=%d body=%q, want the specific route to win over Static", resp.StatusCode, body)
	}
	if resp, body := serve(t, app, "GET", "/assets/main.css", nil); resp.StatusCode != 200 || !strings.HasPrefix(body, "body{") {
		t.Fatalf("/assets/main.css: status=%d body=%q, want Static to serve it", resp.StatusCode, body)
	}
}

// TestStatic_WithIndex serves the index document for directory and root
// requests, and still serves nested files directly.
func TestStatic_WithIndex(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		app := zip.New(zip.Config{DisableStartupMessage: true})
		app.Get("/app/*", zip.Static(assets, zip.WithIndex("index.html")))

		idx, _ := fs.ReadFile(assets, "index.html")
		if resp, body := serve(t, app, "GET", "/app/", nil); resp.StatusCode != 200 || body != string(idx) {
			t.Fatalf("/app/: status=%d body=%q, want index.html %q", resp.StatusCode, body, idx)
		}
		page, _ := fs.ReadFile(assets, "sub/page.html")
		if resp, body := serve(t, app, "GET", "/app/sub/page.html", nil); resp.StatusCode != 200 || body != string(page) {
			t.Fatalf("/app/sub/page.html: status=%d body=%q, want the nested file", resp.StatusCode, body)
		}
	})
}

// TestStatic_WithStripPrefix derives the fs path from the request path minus a
// prefix instead of the "*" capture: a versioned URL served from an
// unversioned tree. The default "*" capture ("v2/main.css") would miss; the
// strip maps it to "main.css".
func TestStatic_WithStripPrefix(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/static/*", zip.Static(embedAssets(t), zip.WithStripPrefix("/static/v2/")))

	if resp, body := serve(t, app, "GET", "/static/v2/main.css", nil); resp.StatusCode != 200 || !strings.HasPrefix(body, "body{") {
		t.Fatalf("/static/v2/main.css: status=%d body=%q, want main.css via strip-prefix", resp.StatusCode, body)
	}
}

// TestStatic_IfModifiedSince304 exercises the conditional-GET path against
// os.DirFS (real mod times; embed.FS mod times are zero so Last-Modified is
// omitted there by design).
func TestStatic_IfModifiedSince304(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/assets/*", zip.Static(dirAssets()))

	resp, _ := serve(t, app, "GET", "/assets/main.css", nil)
	lastMod := resp.Header.Get("Last-Modified")
	if lastMod == "" {
		t.Fatal("os.DirFS serve set no Last-Modified")
	}
	// Re-request with the exact Last-Modified: must be 304 with no body.
	resp2, body2 := serve(t, app, "GET", "/assets/main.css", map[string]string{"If-Modified-Since": lastMod})
	if resp2.StatusCode != 304 {
		t.Fatalf("conditional GET: status %d, want 304", resp2.StatusCode)
	}
	if body2 != "" {
		t.Fatalf("304 body %q, want empty", body2)
	}
}
