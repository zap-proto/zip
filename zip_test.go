package zip_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"

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

// TestOneCtxPerRequest pins the Ctx lifetime rule: one request gets ONE *Ctx,
// handed to every zip handler it passes through — Use middleware, group
// middleware, and the leaf alike. Two things depend on it: enrichment a
// middleware makes (see TestSetLogReachesDownstream) is visible downstream,
// and the wrapper costs one allocation per request instead of one per handler
// (see TestServePathAllocsAreChainDepthInvariant).
func TestOneCtxPerRequest(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	var seen []*zip.Ctx
	app.Use(zip.H(func(c *zip.Ctx) error { seen = append(seen, c); return c.Continue() }))
	v1 := app.Group("/v1")
	v1.Use(zip.H(func(c *zip.Ctx) error { seen = append(seen, c); return c.Continue() }))
	v1.Get("/ping", func(c *zip.Ctx) error {
		seen = append(seen, c)
		return c.NoContent(204)
	})

	req, _ := http.NewRequest("GET", "/v1/ping", nil)
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("ran %d handlers, want 3", len(seen))
	}
	for i, c := range seen {
		if c != seen[0] {
			t.Fatalf("handler %d got a different *Ctx than handler 0", i)
		}
	}

	// The Ctx belongs to the request, so a second request must not be handed
	// the first one's — its fields would be stale.
	first := seen[0]
	seen = nil
	req2, _ := http.NewRequest("GET", "/v1/ping", nil)
	if _, err := app.Fiber().Test(req2); err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("second request ran no handlers")
	}
	if seen[0] == first {
		t.Fatal("a second request reused the first request's *Ctx")
	}
}

// TestSetLogReachesDownstream is what one-Ctx-per-request buys correctness-wise:
// middleware.Logger attaches request_id / org / user with c.SetLog, and the
// handlers after it must actually log through that enriched logger.
func TestSetLogReachesDownstream(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	var scoped, downstream luxlog.Logger
	app.Use(zip.H(func(c *zip.Ctx) error {
		scoped = c.Log().New("request_id", "rid-42")
		c.SetLog(scoped)
		return c.Continue()
	}))
	app.Get("/x", func(c *zip.Ctx) error {
		downstream = c.Log()
		return c.NoContent(204)
	})

	req, _ := http.NewRequest("GET", "/x", nil)
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if scoped == nil || downstream == nil {
		t.Fatal("handlers did not run")
	}
	if downstream != scoped {
		t.Fatal("SetLog in middleware did not reach the downstream handler")
	}
}

// TestServePathAllocsAreChainDepthInvariant pins the serve-path allocation
// budget: the zip wrapper costs ONE heap value per request (the *Ctx) no
// matter how many handlers the chain has. Before one-Ctx-per-request this
// grew linearly — a 5-middleware production stack paid 6.
//
// Each iteration calls ResetUserValues, which is exactly what both transports
// do before dispatching (zap-proto/http's serveConn; fasthttp's keep-alive
// loop via Request.Reset), so one iteration is one real request.
//
// SO THE MEASURE IS THE DELTA, NOT THE TOTAL. This asserted `total <= 1` and was
// therefore red on every commit for as long as anyone can remember, reporting 34
// at every depth — which is the invariant PASSING and the bound failing. A total
// counts everything a request pays, and the wrapper does not own most of it: with
// the logger replaced by luxlog.NewNoOpLogger the figure is 32, so the access log
// is 2, and an alloc profile of the remaining 32 attributes them to TELEMETRY —
// zip.describe with its strings.Builder (~47% of objects, cumulative), the metric
// library's labelsKeyFromLabels (~27%), plus the hex trace/span ids, net.IP.String
// and a strconv.FormatInt. Per-request metrics and spans are a feature, so no
// arrangement of the wrapper reaches one, and a bound nothing can satisfy gates
// nothing: it trains a reader to skip a red line.
//
// The delta is the claim above, it is exactly measurable, and it holds: five
// middlewares cost the same as none. The total is held separately as a RATCHET so
// the constant cannot grow unnoticed — lower it when describe or the label key
// gets cheaper, and raise it only with a reason written down.
func TestServePathAllocsAreChainDepthInvariant(t *testing.T) {
	// Indexed by middleware depth, so the assertions below compare depths rather
	// than each depth against a literal.
	allocs := map[int]float64{}
	for _, mw := range []int{0, 1, 5} {
		app := zip.New(zip.Config{DisableStartupMessage: true, ServerHeader: "-"})
		for i := 0; i < mw; i++ {
			app.Use(zip.H(func(c *zip.Ctx) error { return c.Continue() }))
		}
		app.Get("/v1/health", func(c *zip.Ctx) error { return c.NoContent(204) })
		h := app.Fiber().Handler()

		fctx := &fasthttp.RequestCtx{}
		fctx.Request.Header.SetMethod("GET")
		fctx.URI().SetPath("/v1/health")

		got := testing.AllocsPerRun(2000, func() {
			fctx.ResetUserValues()
			h(fctx)
		})
		if fctx.Response.StatusCode() != 204 {
			t.Fatalf("mw=%d: status %d", mw, fctx.Response.StatusCode())
		}
		allocs[mw] = got
		t.Logf("mw=%d: %.0f allocs/request", mw, got)
	}

	// THE CLAIM: the wrapper allocates per REQUEST, not per HANDLER. Before
	// one-Ctx-per-request a five-middleware stack paid six, so a regression here
	// shows up as a depth-proportional climb.
	for _, mw := range []int{1, 5} {
		if grew := allocs[mw] - allocs[0]; grew > 0.5 {
			t.Errorf("mw=%d costs %.0f allocs against %.0f at mw=0 — the wrapper is "+
				"allocating per handler again (%.0f more for %d handlers)",
				mw, allocs[mw], allocs[0], grew, mw)
		}
	}

	// THE RATCHET: sync.Pool's pinSlow shows up in the profile, so the figure can
	// wobble with GOMAXPROCS; the margin is for that and not for drift.
	const ceiling = 36
	if allocs[0] > ceiling {
		t.Errorf("a bare request costs %.0f allocs, over the %d ceiling — profile it "+
			"(go test -bench . -memprofile) before raising this: the constant is "+
			"telemetry, chiefly zip.describe and the metric label key",
			allocs[0], ceiling)
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
