package zip_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
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
