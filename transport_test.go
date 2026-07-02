package zip_test

import (
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"

	"github.com/zap-proto/zip"
)

// TestListen_ZAP proves a bare address (DefaultScheme) serves over the real ZAP
// transport: a route on the App answers a request over ZAP with the same
// handler/JSON path as HTTP.
func TestListen_ZAP(t *testing.T) {
	app := zip.New(zip.Config{AppName: "zaptest", DisableStartupMessage: true})
	app.Get("/v1/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok", "transport": "zap"})
	})

	const addr = "127.0.0.1:19653"
	go func() { _ = app.Listen(addr) }() // bare addr = ZAP (DefaultScheme)
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

// TestListen_DualTransport proves the decomplected design: ONE Listen call with
// two addresses serves the SAME handler over BOTH transports (ZAP + HTTP), the
// scheme selecting each. This is the headline of "one verb, transport is a value".
func TestListen_DualTransport(t *testing.T) {
	app := zip.New(zip.Config{AppName: "dual", DisableStartupMessage: true})
	app.Get("/v1/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})

	const zapAddr = "127.0.0.1:19654"
	const httpAddr = "127.0.0.1:18080"
	go func() { _ = app.Listen(zapAddr, "http://"+httpAddr) }() // ONE call, both transports
	defer func() { _ = app.Shutdown() }()
	waitReachable(t, zapAddr)

	// ZAP side.
	ztr := zaphttp.NewTransport(zapAddr)
	defer ztr.CloseIdleConnections()
	zreq, zresp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(zreq)
	defer fasthttp.ReleaseResponse(zresp)
	zreq.SetRequestURI("/v1/health")
	if err := ztr.Do(zreq, zresp); err != nil || zresp.StatusCode() != 200 {
		t.Fatalf("ZAP transport: err=%v status=%d", err, zresp.StatusCode())
	}

	// HTTP side — same app, same route, other transport (plain fasthttp client).
	var httpErr error
	for i := 0; i < 50; i++ {
		code, body, err := fasthttp.Get(nil, "http://"+httpAddr+"/v1/health")
		if err == nil && code == 200 && strings.Contains(string(body), `"status":"ok"`) {
			httpErr = nil
			break
		}
		httpErr = err
		time.Sleep(40 * time.Millisecond)
	}
	if httpErr != nil {
		t.Fatalf("HTTP transport never served: %v", httpErr)
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
