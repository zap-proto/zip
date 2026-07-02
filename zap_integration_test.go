package zip_test

import (
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"

	"github.com/zap-proto/zip"
)

// TestListenZAP_RoundTrip proves the un-stubbed ListenZAP actually SERVES: a
// route registered on the App answers a real request over the ZAP transport,
// with the same handler/JSON path as HTTP. (Replaces the old stub that returned
// "ZAP wire dispatch not yet implemented".)
func TestListenZAP_RoundTrip(t *testing.T) {
	app := zip.New(zip.Config{AppName: "zaptest", DisableStartupMessage: true})
	app.Get("/v1/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok", "transport": "zap"})
	})

	const addr = "127.0.0.1:19653"
	go func() { _ = app.ListenZAP(addr) }()
	defer func() { _ = app.Shutdown() }()

	// Wait for the ZAP listener to accept (bounded).
	waitReachable(t, addr)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI("/v1/health")
	req.Header.SetMethod(fasthttp.MethodGet)

	tr := zaphttp.NewTransport(addr)
	defer tr.CloseIdleConnections()
	if err := tr.Do(req, resp); err != nil {
		t.Fatalf("ZAP round-trip failed: %v", err)
	}

	if resp.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode())
	}
	body := string(resp.Body())
	if !strings.Contains(body, `"status":"ok"`) || !strings.Contains(body, `"transport":"zap"`) {
		t.Fatalf("body over ZAP = %q, want the handler's JSON", body)
	}
}

func waitReachable(t *testing.T, addr string) {
	t.Helper()
	tr := zaphttp.NewTransport(addr)
	tr.SetDialTimeout(200 * time.Millisecond)
	defer tr.CloseIdleConnections()
	for i := 0; i < 50; i++ {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		req.SetRequestURI("/v1/health")
		req.Header.SetMethod(fasthttp.MethodGet)
		err := tr.Do(req, resp)
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		if err == nil {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("ZAP listener at %s never became reachable", addr)
}
