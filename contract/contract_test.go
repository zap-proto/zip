package contract_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/o11y/pkg/zaplogreceiver"
	"github.com/hanzoai/o11y/pkg/zapreceiver"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The collector this is proven against is the version cloud runs. Moving the
// pin in go.mod is how this test asks the question again of a newer receiver.

// quiet is the receivers' logger. Their own warnings are this test's noise
// unless something fails, and when something fails the assertion says more.
func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// free returns a loopback address nothing is listening on yet. The receiver
// takes a port out of it and binds; the exporter dials the same host:port.
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

// held is what a receiver handed its writer, kept so a test can read it.
type held[B any] struct {
	mu    sync.Mutex
	batch *B
	seen  chan struct{}
}

func (h *held[B]) take(ctx context.Context, b *B) error {
	h.mu.Lock()
	h.batch = b
	h.mu.Unlock()
	select {
	case h.seen <- struct{}{}:
	default:
	}
	return nil
}

func (h *held[B]) await(t *testing.T) *B {
	t.Helper()
	select {
	case <-h.seen:
	case <-time.After(10 * time.Second):
		t.Fatal("the receiver decoded nothing")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.batch
}

// serveOneRequest is the whole producing side: an app with two addresses, one
// route, one request, and a shutdown that drains what the boundary collected.
// Nothing in it mentions telemetry, which is the claim.
func serveOneRequest(t *testing.T, spans, logs string, req *http.Request) {
	t.Helper()
	app := zip.New(zip.Config{
		AppName:               "proof",
		Logger:                luxlog.NewNoOpLogger(),
		DisableStartupMessage: true,
		Telemetry:             zip.Telemetry{Spans: spans, Logs: logs},
	})
	app.Get("/v1/orders/:id", func(c *zip.Ctx) error { return c.String(200, "ok") })

	h, err := zip.Serve(app, "http://"+free(t))
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer func() { _ = h.Close() }()

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	// Shutdown drains, so the proof does not wait out an export interval.
	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestTheCollectorDecodesWhatZipExports is the proof. Both receivers are the
// collector's own, and everything asserted below is read off the struct THEY
// produced — so a field renamed on either side arrives as a zero value here.
func TestTheCollectorDecodesWhatZipExports(t *testing.T) {
	spanAddr, logAddr := free(t), free(t)

	gotSpans := &held[zapreceiver.SpanBatch]{seen: make(chan struct{}, 4)}
	spanRcv, err := zapreceiver.New(zapreceiver.Config{
		Listen: spanAddr, OnBatch: gotSpans.take, Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("span receiver: %v", err)
	}
	defer spanRcv.Stop()

	gotLogs := &held[zaplogreceiver.LogBatch]{seen: make(chan struct{}, 4)}
	logRcv, err := zaplogreceiver.New(zaplogreceiver.Config{
		Listen: logAddr, OnBatch: gotLogs.take, Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("log receiver: %v", err)
	}
	defer logRcv.Stop()

	req := httptest.NewRequest(http.MethodGet, "/v1/orders/8a3f", nil)
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u-1")
	serveOneRequest(t, spanAddr, logAddr, req)

	t.Run("span", func(t *testing.T) {
		batch := gotSpans.await(t)
		if batch.AppName != "proof" {
			t.Errorf("appName = %q, want proof — the collector lifts it into service.name", batch.AppName)
		}
		if len(batch.Spans) != 1 {
			t.Fatalf("want one span for one request, got %d", len(batch.Spans))
		}
		s := batch.Spans[0]
		if len(s.TraceID) != 32 || len(s.SpanID) != 16 {
			t.Errorf("trace/span ids are %q/%q, want 32/16 hex", s.TraceID, s.SpanID)
		}
		if s.Name != "/v1/orders/:id" {
			t.Errorf("name = %q, want the route template — a trace store groups by it", s.Name)
		}
		if s.Kind != "server" {
			t.Errorf("kind = %q, want server", s.Kind)
		}
		if s.StatusCode != "ok" {
			t.Errorf("statusCode = %q, want ok for a 200", s.StatusCode)
		}
		if s.EndUnixNs <= s.StartUnixNs {
			t.Errorf("span does not span time: start=%d end=%d", s.StartUnixNs, s.EndUnixNs)
		}
		// The keys the collector reads its columns out of. url.path is first in
		// the list it picks a span's path from; a key of our own invention would
		// be stored and would leave that column empty.
		wantAttrs(t, s.Attributes, map[string]any{
			"url.path":                  "/v1/orders/8a3f",
			"http.route":                "/v1/orders/:id",
			"http.request.method":       "GET",
			"http.response.status_code": float64(200),
			"hanzo.org":                 "acme",
			"hanzo.user":                "u-1",
		})
		// The tenant is the operator of this program, never the caller: the
		// collector reads resource["org"] as whose rows these become.
		if _, ok := batch.Resource["org"]; ok {
			t.Errorf("the batch claims tenant %q from a request it merely answered", batch.Resource["org"])
		}
	})

	t.Run("record", func(t *testing.T) {
		batch := gotLogs.await(t)
		if batch.AppName != "proof" {
			t.Errorf("appName = %q, want proof", batch.AppName)
		}
		if len(batch.Records) != 1 {
			t.Fatalf("want one record for one request, got %d", len(batch.Records))
		}
		r := batch.Records[0]
		if r.Body != "request" {
			t.Errorf("body = %q, want request — one line per request, the same line the app logs", r.Body)
		}
		if r.Severity != 9 || r.SeverityText != "info" {
			t.Errorf("severity = %d/%q, want 9/info for a 200", r.Severity, r.SeverityText)
		}
		if r.TimeUnixNs == 0 {
			t.Error("the record has no time, so nothing can order it")
		}
		// The ids are why a log address is worth setting: they put this line
		// inside the span above rather than beside it.
		if len(r.TraceID) != 32 || len(r.SpanID) != 16 {
			t.Errorf("record ids are %q/%q, want 32/16 hex", r.TraceID, r.SpanID)
		}
		wantAttrs(t, r.Attributes, map[string]any{
			"url.path":                  "/v1/orders/8a3f",
			"http.route":                "/v1/orders/:id",
			"http.request.method":       "GET",
			"http.response.status_code": float64(200),
			"hanzo.org":                 "acme",
			"hanzo.user":                "u-1",
		})
		if _, ok := r.Attributes["duration_ms"]; !ok {
			t.Error("the record has no duration, which is the field the log line exists to carry")
		}
	})
}

// TestAFailedRequestArrivesAsAFailure pins the one thing an operator filters on.
// A span that says ok for a 500, or a record at info, is worse than no telemetry:
// it answers the question wrongly.
func TestAFailedRequestArrivesAsAFailure(t *testing.T) {
	spanAddr, logAddr := free(t), free(t)

	gotSpans := &held[zapreceiver.SpanBatch]{seen: make(chan struct{}, 4)}
	spanRcv, err := zapreceiver.New(zapreceiver.Config{
		Listen: spanAddr, OnBatch: gotSpans.take, Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("span receiver: %v", err)
	}
	defer spanRcv.Stop()

	gotLogs := &held[zaplogreceiver.LogBatch]{seen: make(chan struct{}, 4)}
	logRcv, err := zaplogreceiver.New(zaplogreceiver.Config{
		Listen: logAddr, OnBatch: gotLogs.take, Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("log receiver: %v", err)
	}
	defer logRcv.Stop()

	app := zip.New(zip.Config{
		AppName:               "proof",
		Logger:                luxlog.NewNoOpLogger(),
		DisableStartupMessage: true,
		Telemetry:             zip.Telemetry{Spans: spanAddr, Logs: logAddr},
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

	if s := gotSpans.await(t).Spans[0]; s.StatusCode != "error" {
		t.Errorf("statusCode = %q, want error for a 500", s.StatusCode)
	}
	r := gotLogs.await(t).Records[0]
	if r.Severity != 17 || r.SeverityText != "error" {
		t.Errorf("severity = %d/%q, want 17/error for a 500", r.Severity, r.SeverityText)
	}
}

// wantAttrs checks every key the collector reads a column out of, and reports
// the ones that arrived under some other name.
func wantAttrs(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attribute %q = %v (%T), want %v", k, got[k], got[k], v)
		}
	}
}
