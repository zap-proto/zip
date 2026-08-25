package zip_test

import (
	"io/fs"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// These tests pin zip.Static's byte-range answers (RFC 9110 §14). The property
// that matters is the three-way split: a range it HONOURS is 206 with the bytes
// asked for, a range it DECLINES is 200 with the whole file — always legal, and
// what a multi-range request gets — and a range that names bytes the file does
// not have is 416, which is a refusal and must not be answered with the file.
//
// Both fixtures run through bothFS, so whatever embed.FS and os.DirFS disagree
// about shows up here rather than in production.

func rangeApp(assets fs.FS) *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/assets/*", zip.Static(assets))
	return app
}

func TestStatic_RangeServesExactlyTheBytesAsked(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		want, _ := fs.ReadFile(assets, "main.css")
		size := len(want)

		for _, tc := range []struct {
			name, spec string
			from, to   int // inclusive, resolved against size
		}{
			{"prefix", "bytes=0-3", 0, 3},
			{"middle", "bytes=4-8", 4, 8},
			{"open ended", "bytes=5-", 5, size - 1},
			{"suffix", "bytes=-4", size - 4, size - 1},
			{"past the end clamps", "bytes=10-9999", 10, size - 1},
			{"whole file by span", "bytes=0-" + strconv.Itoa(size-1), 0, size - 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resp, body := serve(t, rangeApp(assets), "GET", "/assets/main.css",
					map[string]string{"Range": tc.spec})
				if resp.StatusCode != http.StatusPartialContent {
					t.Fatalf("status %d, want 206", resp.StatusCode)
				}
				if got, w := body, string(want[tc.from:tc.to+1]); got != w {
					t.Fatalf("body %q, want %q", got, w)
				}
				wantCR := "bytes " + strconv.Itoa(tc.from) + "-" + strconv.Itoa(tc.to) + "/" + strconv.Itoa(size)
				if got := resp.Header.Get("Content-Range"); got != wantCR {
					t.Fatalf("Content-Range %q, want %q", got, wantCR)
				}
				if got, w := resp.Header.Get("Content-Length"), strconv.Itoa(tc.to-tc.from+1); got != w {
					t.Fatalf("Content-Length %q, want %q", got, w)
				}
			})
		}
	})
}

func TestStatic_AcceptRangesIsAdvertised(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		resp, _ := serve(t, rangeApp(assets), "GET", "/assets/main.css", nil)
		if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
			t.Fatalf("Accept-Ranges %q, want %q — a client cannot ask for a range it is not told about", got, "bytes")
		}
	})
}

// A span wholly outside the file is a REFUSAL. Answering it with the file would
// hand a client bytes it did not ask for and call that success.
func TestStatic_UnsatisfiableRangeIsRefusedNotServed(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		want, _ := fs.ReadFile(assets, "main.css")
		size := len(want)
		for _, spec := range []string{"bytes=" + strconv.Itoa(size) + "-", "bytes=9999-", "bytes=-0"} {
			resp, body := serve(t, rangeApp(assets), "GET", "/assets/main.css",
				map[string]string{"Range": spec})
			if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("%s: status %d, want 416", spec, resp.StatusCode)
			}
			if body != "" {
				t.Fatalf("%s: body %q, want empty", spec, body)
			}
			if got, w := resp.Header.Get("Content-Range"), "bytes */"+strconv.Itoa(size); got != w {
				t.Fatalf("%s: Content-Range %q, want %q", spec, got, w)
			}
		}
	})
}

// Declining a range is always legal, so these answer the whole file rather than
// failing. Multi-range is the case a client actually sends.
func TestStatic_DeclinedRangeServesTheWholeFile(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		want, _ := fs.ReadFile(assets, "main.css")
		for _, spec := range []string{"bytes=0-1,4-5", "items=0-3", "bytes=abc-def", "bytes=", "not a range"} {
			resp, body := serve(t, rangeApp(assets), "GET", "/assets/main.css",
				map[string]string{"Range": spec})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: status %d, want 200", spec, resp.StatusCode)
			}
			if body != string(want) {
				t.Fatalf("%s: body %q, want the whole file %q", spec, body, want)
			}
			if cr := resp.Header.Get("Content-Range"); cr != "" {
				t.Fatalf("%s: Content-Range %q on a 200, want none", spec, cr)
			}
		}
	})
}

// If-Range is the guard against splicing two versions of a file together: the
// range is honoured only when the representation has not moved.
func TestStatic_IfRangeDecidesWhetherTheRangeApplies(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		want, _ := fs.ReadFile(assets, "main.css")
		app := rangeApp(assets)

		full, _ := serve(t, app, "GET", "/assets/main.css", nil)
		mod := full.Header.Get("Last-Modified")
		if mod == "" {
			t.Skip("no Last-Modified on this fs, so If-Range has nothing to match")
		}

		resp, body := serve(t, app, "GET", "/assets/main.css",
			map[string]string{"Range": "bytes=0-3", "If-Range": mod})
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("matching If-Range: status %d, want 206", resp.StatusCode)
		}
		if body != string(want[:4]) {
			t.Fatalf("matching If-Range: body %q, want %q", body, want[:4])
		}

		stale := time.Now().Add(-72 * time.Hour).UTC().Format(http.TimeFormat)
		resp, body = serve(t, app, "GET", "/assets/main.css",
			map[string]string{"Range": "bytes=0-3", "If-Range": stale})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stale If-Range: status %d, want 200 (whole file)", resp.StatusCode)
		}
		if body != string(want) {
			t.Fatalf("stale If-Range: body %q, want the whole file", body)
		}
	})
}

// A 304 outranks a range: the client already holds the representation, so there
// is nothing to send a piece of.
func TestStatic_NotModifiedOutranksARange(t *testing.T) {
	bothFS(t, func(t *testing.T, assets fs.FS) {
		app := rangeApp(assets)
		full, _ := serve(t, app, "GET", "/assets/main.css", nil)
		mod := full.Header.Get("Last-Modified")
		if mod == "" {
			t.Skip("no Last-Modified on this fs")
		}
		resp, body := serve(t, app, "GET", "/assets/main.css",
			map[string]string{"Range": "bytes=0-3", "If-Modified-Since": mod})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("status %d, want 304", resp.StatusCode)
		}
		if body != "" {
			t.Fatalf("body %q on a 304, want empty", body)
		}
	})
}
