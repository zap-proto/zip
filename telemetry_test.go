package zip_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/luxfi/zap"
	"github.com/zap-proto/zip"
)

// Telemetry is asserted the way an operator reads it: through the log the app
// writes and the text /metrics serves. Nothing here reaches inside zip for a
// counter — if a number cannot be read off the surface a scrape reads, it is
// not instrumented, however correct the code behind it looks.

// sink is a luxfi/log destination that keeps the lines. The logger is the
// app's own — Config.Logger, the one field a zip user configures — so what this
// captures is exactly what a deployment's logger would receive.
type sink struct {
	mu    sync.Mutex
	lines []map[string]any
}

func (s *sink) Write(p []byte) (int, error) {
	var line map[string]any
	if err := json.Unmarshal(p, &line); err == nil {
		s.mu.Lock()
		s.lines = append(s.lines, line)
		s.mu.Unlock()
	}
	return len(p), nil
}

// find returns the first line whose message is msg.
func (s *sink) find(msg string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if l["message"] == msg {
			return l
		}
	}
	return nil
}

// served composes an app with a captured logger, serves one request, and
// returns the sink and the app so both surfaces can be read.
func served(t *testing.T, req *http.Request, register func(*zip.App)) (*sink, *zip.App) {
	t.Helper()
	s := &sink{}
	app := zip.New(zip.Config{
		AppName:               "telemetry-test",
		Logger:                luxlog.NewWriter(s),
		DisableStartupMessage: true,
	})
	register(app)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("serving the request failed: %v", err)
	}
	defer resp.Body.Close()
	return s, app
}

// scrape reads the app's /metrics the way Prometheus does — off the ops app,
// over HTTP — rather than from the registry directly.
func scrape(t *testing.T, app *zip.App) string {
	t.Helper()
	resp, err := app.Ops().Test(httptest.NewRequest(http.MethodGet, zip.MetricsPath, nil))
	if err != nil {
		t.Fatalf("scraping %s failed: %v", zip.MetricsPath, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the scrape failed: %v", err)
	}
	return string(body)
}

// TestReport is the whole claim in one test: an app that does nothing but
// register a route logs the request and moves the RED numbers. There is no
// Use, no middleware and no telemetry call anywhere in it — that absence is
// the assertion.
func TestReport(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/orders/8a3f", nil)
	s, app := served(t, req, func(app *zip.App) {
		app.Get("/v1/orders/:id", func(c *zip.Ctx) error { return c.String(200, "ok") })
	})

	line := s.find("request")
	if line == nil {
		t.Fatalf("no request line was logged; the app logged %d lines", len(s.lines))
	}
	if got := line["path"]; got != "/v1/orders/8a3f" {
		t.Errorf("path = %v, want the path that arrived", got)
	}
	if got := line["method"]; got != "GET" {
		t.Errorf("method = %v, want GET", got)
	}
	if got, ok := line["status"].(float64); !ok || int(got) != 200 {
		t.Errorf("status = %v, want 200", line["status"])
	}
	// The duration must be PRESENT. Its value is a clock reading, so the
	// assertion is that the field exists and is a number — a missing duration is
	// the defect, a duration of 0ms on a handler this small is not.
	if _, ok := line["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms = %v, want a number on every request line", line["duration_ms"])
	}
	if line["trace"] == "" || line["trace"] == nil {
		t.Errorf("trace = %v, want the trace this request belongs to", line["trace"])
	}

	// The RED numbers, read off the scrape surface. The route label is the
	// TEMPLATE, not the path — that is what keeps the metric bounded, so it is
	// asserted rather than assumed.
	text := scrape(t, app)
	want := `http_requests_total{method="GET",route="/v1/orders/:id",status="200"} 1`
	if !strings.Contains(text, want) {
		t.Errorf("the request counter did not move.\nwant a line: %s\ngot:\n%s", want, text)
	}
	if !strings.Contains(text, "http_request_duration_seconds") {
		t.Errorf("no duration histogram in the scrape:\n%s", text)
	}
}

// TestReportCarriesTheCaller proves the log line names who the request was for
// when the environment parked an identity on it, and says nothing when it did
// not. An absent field and an empty one are different answers.
func TestReportCarriesTheCaller(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u-1")
	s, _ := served(t, req, func(app *zip.App) {
		app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })
	})
	line := s.find("request")
	if line == nil {
		t.Fatal("no request line was logged")
	}
	if got := line["org"]; got != "acme" {
		t.Errorf("org = %v, want acme", got)
	}
	if got := line["user"]; got != "u-1" {
		t.Errorf("user = %v, want u-1", got)
	}

	bare, _ := served(t, httptest.NewRequest(http.MethodGet, "/x", nil), func(app *zip.App) {
		app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })
	})
	line = bare.find("request")
	if line == nil {
		t.Fatal("no request line was logged for the anonymous request")
	}
	if _, present := line["org"]; present {
		t.Errorf("org = %v on a request no gateway touched; it must be absent, not empty", line["org"])
	}
}

// TestReportJoinsAnIncomingTrace proves trace context is propagated rather than
// restarted: a request arriving with a traceparent stays in that trace, and the
// header this hop leaves on the request names a NEW span under it — which is
// what the next hop reads as its parent.
func TestReportJoinsAnIncomingTrace(t *testing.T) {
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	const parent = "00f067aa0ba902b7"

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(zip.HeaderTrace, "00-"+id+"-"+parent+"-01")

	var onward string
	s, _ := served(t, req, func(app *zip.App) {
		app.Get("/x", func(c *zip.Ctx) error {
			onward = c.Header(zip.HeaderTrace)
			return c.String(200, "ok")
		})
	})

	line := s.find("request")
	if line == nil {
		t.Fatal("no request line was logged")
	}
	if line["trace"] != id {
		t.Errorf("trace = %v, want the incoming trace %s — a new one means the trace broke here", line["trace"], id)
	}
	if line["span"] == parent {
		t.Error("this hop reused the caller's span id; it must mint its own")
	}
	if !strings.HasPrefix(onward, "00-"+id+"-") {
		t.Errorf("the header a handler forwards is %q, want it to carry trace %s", onward, id)
	}
	if strings.Contains(onward, parent) {
		t.Errorf("the header a handler forwards still names the caller's span: %q", onward)
	}
}

// TestReportStartsATraceWhenNoneArrives proves the edge case the edge depends
// on: a request with no trace context begins one rather than reporting none.
func TestReportStartsATraceWhenNoneArrives(t *testing.T) {
	s, _ := served(t, httptest.NewRequest(http.MethodGet, "/x", nil), func(app *zip.App) {
		app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })
	})
	line := s.find("request")
	if line == nil {
		t.Fatal("no request line was logged")
	}
	id, _ := line["trace"].(string)
	if len(id) != 32 {
		t.Errorf("trace = %q, want a minted 32-hex trace id", id)
	}
}

// TestReportCountsWhatMatchedNothing proves the boundary is outermost. A 404 is
// a request the service answered, and it is the one an operator most needs to
// see; a report composed into route chains would miss it entirely.
func TestReportCountsWhatMatchedNothing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	s, app := served(t, req, func(app *zip.App) {
		app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })
	})
	if s.find("request") == nil {
		t.Fatal("a request that matched no route was not logged")
	}
	// The route label is empty, not "/nope": an unrouted path is client-supplied
	// and unbounded, and putting it in a label is how a metric store falls over.
	want := `http_requests_total{method="GET",route="",status="404"} 1`
	if text := scrape(t, app); !strings.Contains(text, want) {
		t.Errorf("the 404 was not counted under an empty route.\nwant: %s\ngot:\n%s", want, text)
	}
}

// TestReportLevelsByOutcome proves an error level means this service failed. A
// 4xx is a client being refused and must not raise the level, or filtering on
// Error stops selecting anything worth waking up for.
func TestReportLevelsByOutcome(t *testing.T) {
	for _, c := range []struct {
		name  string
		serve zip.Handler
		level string
	}{
		{"ok", func(c *zip.Ctx) error { return c.String(200, "ok") }, "info"},
		{"refused", func(c *zip.Ctx) error { return zip.ErrForbidden("no") }, "error"},
		{"failed", func(c *zip.Ctx) error { return c.String(500, "boom") }, "error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := served(t, httptest.NewRequest(http.MethodGet, "/x", nil), func(app *zip.App) {
				app.Get("/x", c.serve)
			})
			line := s.find("request")
			if line == nil {
				t.Fatal("no request line was logged")
			}
			// A handler that RETURNS an error reports the failure it returned; the
			// status the error handler then wrote is a consequence, not the outcome.
			if c.name == "refused" {
				if line["error"] == nil {
					t.Errorf("a returned error was not named on the line: %v", line)
				}
				return
			}
			if got := line["level"]; got != c.level {
				t.Errorf("level = %v, want %v", got, c.level)
			}
		})
	}
}

// TestOpsIsNotTraffic proves probes and scrapes stay out of the numbers. A
// liveness check every second would otherwise be most of the rate, and the
// duration histogram would describe the kubelet rather than the service.
func TestOpsIsNotTraffic(t *testing.T) {
	_, app := served(t, httptest.NewRequest(http.MethodGet, "/x", nil), func(app *zip.App) {
		app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })
	})
	ops := app.Ops()
	for _, p := range []string{zip.HealthPath, zip.ReadyPath} {
		resp, err := ops.Test(httptest.NewRequest(http.MethodGet, p, nil))
		if err != nil {
			t.Fatalf("probing %s failed: %v", p, err)
		}
		resp.Body.Close()
	}
	text := scrape(t, app)
	for _, p := range []string{zip.HealthPath, zip.ReadyPath, zip.MetricsPath} {
		if strings.Contains(text, `route="`+p+`"`) {
			t.Errorf("%s was counted as traffic:\n%s", p, text)
		}
	}
}

// TestTelemetryOff proves the one switch is the only switch, and that it is
// complete: an app that must not emit logs no line and publishes no numbers.
func TestTelemetryOff(t *testing.T) {
	s := &sink{}
	app := zip.New(zip.Config{
		AppName:               "silent",
		Logger:                luxlog.NewWriter(s),
		DisableStartupMessage: true,
		Telemetry:             zip.Telemetry{Off: true},
	})
	app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil {
		t.Fatalf("serving failed: %v", err)
	}
	resp.Body.Close()
	if line := s.find("request"); line != nil {
		t.Errorf("an app with telemetry off still logged a request line: %v", line)
	}
	if text := scrape(t, app); strings.Contains(text, "http_requests_total") {
		t.Errorf("an app with telemetry off still published numbers:\n%s", text)
	}
}

// --- the export leg -------------------------------------------------------

// collector is a ZAP listener standing in for o11y, decoding the envelope the
// way its receivers do.
type collector struct {
	node *zap.Node
	mu   sync.Mutex
	body [][]byte
	seen chan struct{}
}

func collect(t *testing.T, addr string, msg uint16) *collector {
	t.Helper()
	c := &collector{seen: make(chan struct{}, 8)}
	c.node = zap.NewNode(zap.NodeConfig{
		NodeID: "collector", ServiceType: "_o11y._tcp", Address: addr, NoDiscovery: true,
	})
	c.node.Handle(msg, func(_ context.Context, _ string, m *zap.Message) (*zap.Message, error) {
		cp := append([]byte(nil), m.Root().Bytes(0)...)
		c.mu.Lock()
		c.body = append(c.body, cp)
		c.mu.Unlock()
		select {
		case c.seen <- struct{}{}:
		default:
		}
		return nil, nil
	})
	if err := c.node.Start(); err != nil {
		t.Fatalf("collector on %s: %v", addr, err)
	}
	t.Cleanup(c.node.Stop)
	return c
}

func (c *collector) await(t *testing.T) []byte {
	t.Helper()
	select {
	case <-c.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the collector")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body[len(c.body)-1]
}

// free returns a loopback address nothing is listening on yet. host:port is the
// only address form the collector serves, so it is the only one tested.
func free(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// TestBoundaryExportsASpan is the end-to-end claim: an app registers one route,
// answers one request, and a span for that request arrives at a collector — with
// no telemetry code in the app at all.
func TestBoundaryExportsASpan(t *testing.T) {
	addr := free(t)
	got := collect(t, addr, 1) // 1 = spans, the fleet-wide message type

	app := zip.New(zip.Config{
		AppName:               "boundary",
		Logger:                luxlog.NewWriter(&sink{}),
		DisableStartupMessage: true,
		Telemetry:             zip.Telemetry{Spans: addr},
	})
	app.Get("/v1/orders/:id", func(c *zip.Ctx) error { return c.String(200, "ok") })

	h, err := zip.Serve(app, "http://"+free(t))
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer func() { _ = h.Close() }()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/orders/8a3f", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	// Shutdown drains what the boundary collected, so the test does not wait out
	// an export interval to see the span it just caused.
	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	var batch struct {
		Spans []struct {
			TraceID    string         `json:"traceId"`
			SpanID     string         `json:"spanId"`
			Name       string         `json:"name"`
			Kind       string         `json:"kind"`
			StatusCode string         `json:"statusCode"`
			Start      int64          `json:"startUnixNs"`
			End        int64          `json:"endUnixNs"`
			Attributes map[string]any `json:"attributes"`
		} `json:"spans"`
	}
	if err := json.Unmarshal(got.await(t), &batch); err != nil {
		t.Fatalf("collector could not decode the batch: %v", err)
	}
	if len(batch.Spans) != 1 {
		t.Fatalf("want one span for one request, got %d", len(batch.Spans))
	}
	s := batch.Spans[0]
	if len(s.TraceID) != 32 || len(s.SpanID) != 16 {
		t.Errorf("trace/span ids are %q/%q, want 32/16 hex", s.TraceID, s.SpanID)
	}
	// The name is the TEMPLATE, so a trace store can group by operation.
	if s.Name != "/v1/orders/:id" {
		t.Errorf("name = %q, want the route template", s.Name)
	}
	if s.Kind != "server" {
		t.Errorf("kind = %q, want server", s.Kind)
	}
	if s.StatusCode != "ok" {
		t.Errorf("statusCode = %q, want ok for a 200", s.StatusCode)
	}
	if s.End <= s.Start {
		t.Errorf("span does not span time: start=%d end=%d", s.Start, s.End)
	}
	// The path rides as an ATTRIBUTE, where cardinality belongs, under the key
	// the collector reads a span's path column out of.
	if s.Attributes["url.path"] != "/v1/orders/8a3f" {
		t.Errorf("url.path = %v, want the path that arrived", s.Attributes["url.path"])
	}
}

// TestBoundaryExportsALog proves the same for the log leg, and that the record
// carries the ids that join it to its span.
func TestBoundaryExportsALog(t *testing.T) {
	addr := free(t)
	got := collect(t, addr, 3) // 3 = logs

	app := zip.New(zip.Config{
		AppName:               "boundary",
		Logger:                luxlog.NewWriter(&sink{}),
		DisableStartupMessage: true,
		Telemetry:             zip.Telemetry{Logs: addr},
	})
	app.Get("/x", func(c *zip.Ctx) error { return c.String(500, "boom") })

	h, err := zip.Serve(app, "http://"+free(t))
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer func() { _ = h.Close() }()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	var batch struct {
		Records []struct {
			Body     string `json:"body"`
			Severity int    `json:"severity"`
			Text     string `json:"severityText"`
			TraceID  string `json:"traceId"`
			SpanID   string `json:"spanId"`
		} `json:"records"`
	}
	if err := json.Unmarshal(got.await(t), &batch); err != nil {
		t.Fatalf("collector could not decode the batch: %v", err)
	}
	if len(batch.Records) != 1 {
		t.Fatalf("want one record for one request, got %d", len(batch.Records))
	}
	r := batch.Records[0]
	if r.Body != "request" {
		t.Errorf("body = %q, want request", r.Body)
	}
	// A 500 is this service failing, so the record must carry error severity —
	// 17 is OTel's number for it.
	if r.Severity != 17 || r.Text != "error" {
		t.Errorf("severity = %d/%q, want 17/error for a 500", r.Severity, r.Text)
	}
	if len(r.TraceID) != 32 || len(r.SpanID) != 16 {
		t.Errorf("record lost its trace ids: %q/%q", r.TraceID, r.SpanID)
	}
}

// TestTheEnvironmentStatesTheAddress is how a deployment turns export on: the
// manifest sets a variable, the program sets no field, and the span arrives.
// This is the path cloud's own manifest takes, so the name is asserted here as
// the deployment spells it.
func TestTheEnvironmentStatesTheAddress(t *testing.T) {
	addr := free(t)
	got := collect(t, addr, 1)
	t.Setenv("O11Y_SPANS_ADDR", addr)

	app := zip.New(zip.Config{
		AppName:               "fromenv",
		Logger:                luxlog.NewWriter(&sink{}),
		DisableStartupMessage: true,
		// No Telemetry field at all: the environment is the whole configuration.
	})
	app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })

	h, err := zip.Serve(app, "http://"+free(t))
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer func() { _ = h.Close() }()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	var batch struct {
		Spans []struct {
			Name string `json:"name"`
		} `json:"spans"`
	}
	if err := json.Unmarshal(got.await(t), &batch); err != nil {
		t.Fatalf("collector could not decode: %v", err)
	}
	if len(batch.Spans) != 1 || batch.Spans[0].Name != "/x" {
		t.Fatalf("want one span named /x, got %+v", batch.Spans)
	}
}

// TestNoAddressIsSilent is the other half of the switch: a program with nothing
// stated serves normally, keeps logging, and says nothing about telemetry —
// because it never tried. A line about export in a program that exports nothing
// would be the symptom of a connection nobody asked for.
func TestNoAddressIsSilent(t *testing.T) {
	for _, name := range []string{"O11Y_SPANS_ADDR", "O11Y_LOGS_ADDR", "O11Y_METRICS_ADDR"} {
		t.Setenv(name, "")
	}

	s := &sink{}
	app := zip.New(zip.Config{
		AppName:               "lonely",
		Logger:                luxlog.NewWriter(s),
		DisableStartupMessage: true,
	})
	app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })

	h, err := zip.Serve(app, "http://"+free(t))
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer func() { _ = h.Close() }()

	for range 25 {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/x", nil))
		if err != nil {
			t.Fatalf("request failed with no collector: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status %d with no collector, want 200", resp.StatusCode)
		}
		resp.Body.Close()
	}
	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if s.find("request") == nil {
		t.Error("an app with no collector stopped logging")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if msg, _ := l["message"].(string); strings.HasPrefix(msg, "telemetry export") {
			t.Errorf("an app with no address said %q; it should have dialled nothing", msg)
		}
	}
}

// TestTheCollectorBeingDownCostsNothing is the fire-and-forget claim at the app
// level: an address that nothing answers must not fail a request, and must be
// reported once rather than once per flush.
func TestTheCollectorBeingDownCostsNothing(t *testing.T) {
	s := &sink{}
	app := zip.New(zip.Config{
		AppName:               "shouting",
		Logger:                luxlog.NewWriter(s),
		DisableStartupMessage: true,
		Telemetry:             zip.Telemetry{Spans: free(t)},
	})
	app.Get("/x", func(c *zip.Ctx) error { return c.String(200, "ok") })

	h, err := zip.Serve(app, "http://"+free(t))
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer func() { _ = h.Close() }()

	for range 25 {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/x", nil))
		if err != nil {
			t.Fatalf("request failed with a dead collector: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status %d with a dead collector, want 200", resp.StatusCode)
		}
		resp.Body.Close()
	}
	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stopped := 0
	for _, l := range s.lines {
		if l["message"] == "telemetry export stopped" {
			stopped++
		}
	}
	if stopped != 1 {
		t.Errorf("said %d times that export is down; edge-triggered means once per signal", stopped)
	}
}

// An instrument registered through Metrics rides the same registry as the
// framework's own, so it appears in Gather and therefore on /metrics and in the
// export — the whole point of one registry per program.
func TestMetricsRegistersIntoTheOneRegistry(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.NewWriter(&sink{})})
	g := app.Metrics().NewGaugeVec("fleet_up", "probe verdict per service", []string{"service"})
	g.WithLabelValues("iam").Set(1)

	fams, err := app.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range fams {
		if f.Name == "fleet_up" {
			return
		}
	}
	t.Fatal("fleet_up not gathered — Metrics() must register into the app's own registry")
}
