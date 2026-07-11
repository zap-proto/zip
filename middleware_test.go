package zip_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// These tests pin the composable Middleware surface (Middleware/Chain/With),
// which is ADDITIVE to Use: Use registers global c.Next() middleware; With
// wraps a single leaf handler at registration. The two coexist and compose.

func mwApp() *zip.App {
	return zip.New(zip.Config{DisableStartupMessage: true})
}

// order returns a Middleware that appends its tag to *rec on the way in, then
// calls next. It never short-circuits.
func order(rec *[]string, tag string) zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			*rec = append(*rec, tag)
			return next(c)
		}
	}
}

// TestChain_NestsLeftToRight proves Chain(a,b,c) executes a outermost, then b,
// then c, then the handler — a(b(c(handler))).
func TestChain_NestsLeftToRight(t *testing.T) {
	app := mwApp()
	var rec []string
	app.With(order(&rec, "a"), order(&rec, "b"), order(&rec, "c")).
		Get("/x", func(c *zip.Ctx) error {
			rec = append(rec, "handler")
			return c.String(200, "ok")
		})

	if resp, body := doGet(t, app, "/x"); resp.StatusCode != 200 || body != "ok" {
		t.Fatalf("status=%d body=%q, want 200 ok", resp.StatusCode, body)
	}
	if got := strings.Join(rec, ","); got != "a,b,c,handler" {
		t.Fatalf("execution order %q, want a,b,c,handler", got)
	}
}

// TestChain_Value proves Chain composes into a reusable Middleware value that
// behaves identically to passing the parts to With.
func TestChain_Value(t *testing.T) {
	app := mwApp()
	var rec []string
	pipeline := zip.Chain(order(&rec, "outer"), order(&rec, "inner"))
	app.With(pipeline).Get("/y", func(c *zip.Ctx) error {
		rec = append(rec, "handler")
		return c.String(200, "ok")
	})

	_, _ = doGet(t, app, "/y")
	if got := strings.Join(rec, ","); got != "outer,inner,handler" {
		t.Fatalf("execution order %q, want outer,inner,handler", got)
	}
}

// --- The cloud proof --------------------------------------------------------
//
// cmd/cloud today hand-nests its per-route middleware:
//
//	s.rateLimit(s.requireCSRF(s.mintKey))
//
// This test proves the identical pipeline via app.With(RateLimit, CSRF).Post,
// asserting (1) execution order — RateLimit outer, CSRF inner, handler last —
// and (2) short-circuit — a RateLimit rejection stops before CSRF and the
// handler ever run. This is why With/Chain earns its place: the nesting reads
// left-to-right at the call site instead of inside-out.

// rateLimit is a realistic guard middleware: when blocked it writes 429 and
// does NOT call next (short-circuit).
func rateLimit(rec *[]string, block bool) zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			*rec = append(*rec, "ratelimit")
			if block {
				return c.String(http.StatusTooManyRequests, "rate limited")
			}
			return next(c)
		}
	}
}

func requireCSRF(rec *[]string) zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			*rec = append(*rec, "csrf")
			return next(c)
		}
	}
}

func TestWith_CloudRateLimitCSRF_Order(t *testing.T) {
	app := mwApp()
	var rec []string
	app.With(rateLimit(&rec, false), requireCSRF(&rec)).
		Post("/v1/keys", func(c *zip.Ctx) error {
			rec = append(rec, "mintkey")
			return c.String(200, "minted")
		})

	if resp, body := doPost(t, app, "/v1/keys"); resp.StatusCode != 200 || body != "minted" {
		t.Fatalf("status=%d body=%q, want 200 minted", resp.StatusCode, body)
	}
	if got := strings.Join(rec, ","); got != "ratelimit,csrf,mintkey" {
		t.Fatalf("order %q, want ratelimit,csrf,mintkey (RateLimit outer, CSRF inner, handler last)", got)
	}
}

func TestWith_CloudRateLimit_ShortCircuits(t *testing.T) {
	app := mwApp()
	var rec []string
	app.With(rateLimit(&rec, true), requireCSRF(&rec)).
		Post("/v1/keys", func(c *zip.Ctx) error {
			rec = append(rec, "mintkey")
			return c.String(200, "minted")
		})

	resp, body := doPost(t, app, "/v1/keys")
	if resp.StatusCode != http.StatusTooManyRequests || body != "rate limited" {
		t.Fatalf("status=%d body=%q, want 429 \"rate limited\"", resp.StatusCode, body)
	}
	if got := strings.Join(rec, ","); got != "ratelimit" {
		t.Fatalf("order %q, want just ratelimit — CSRF and handler must NOT run after a short-circuit", got)
	}
}

// TestWith_CoexistsWithUse proves the two models compose: a global Use
// middleware still runs (in declaration order, via c.Next) before a
// With-wrapped leaf.
func TestWith_CoexistsWithUse(t *testing.T) {
	app := mwApp()
	var rec []string
	app.Use(func(c *zip.Ctx) error { rec = append(rec, "use"); return c.Next() })
	app.With(order(&rec, "with")).Get("/z", func(c *zip.Ctx) error {
		rec = append(rec, "handler")
		return c.String(200, "ok")
	})

	_, _ = doGet(t, app, "/z")
	if got := strings.Join(rec, ","); got != "use,with,handler" {
		t.Fatalf("order %q, want use,with,handler (global Use before per-route With)", got)
	}
}

// TestWith_SpecificityPreserved proves a With-wrapped wildcard still loses to a
// later, more-specific plain route — With changes the handler, not routing.
func TestWith_SpecificityPreserved(t *testing.T) {
	app := mwApp()
	var rec []string
	app.With(order(&rec, "mw")).Get("/api/*", func(c *zip.Ctx) error { return c.String(200, "wild") })
	app.Get("/api/health", func(c *zip.Ctx) error { return c.String(200, "health") })

	if resp, body := doGet(t, app, "/api/health"); resp.StatusCode != 200 || body != "health" {
		t.Fatalf("/api/health: status=%d body=%q, want the specific route (not the With-wrapped wildcard)", resp.StatusCode, body)
	}
	if len(rec) != 0 {
		t.Fatalf("With middleware ran for /api/health (%v); the specific route must win outright", rec)
	}
	if resp, body := doGet(t, app, "/api/other"); resp.StatusCode != 200 || body != "wild" {
		t.Fatalf("/api/other: status=%d body=%q, want the wildcard", resp.StatusCode, body)
	}
}

// --- helpers ----------------------------------------------------------------

func doGet(t *testing.T, app *zip.App, target string) (*http.Response, string) {
	return doReq(t, app, "GET", target)
}

func doPost(t *testing.T, app *zip.App, target string) (*http.Response, string) {
	return doReq(t, app, "POST", target)
}

func doReq(t *testing.T, app *zip.App, method, target string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("NewRequest(%s %s): %v", method, target, err)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%s %s): %v", method, target, err)
	}
	b := make([]byte, 512)
	n, _ := resp.Body.Read(b)
	_ = resp.Body.Close()
	return resp, string(b[:n])
}
