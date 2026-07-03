package zip_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
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

// TestHTTPTransport_ReadBufferSize_Raises431Ceiling is the regression test for
// the bug where zip's HTTP transport built a bare fasthttp.Server and dropped
// zip.Config.ReadBufferSize — capping request headers at fasthttp's 4 KiB
// default and returning 431 (Request Header Fields Too Large) once a browser's
// multi-domain SSO cookies crossed ~4 KiB. It proves both halves: the default
// still 431s a 9 KiB header (fail-visible if the transport ever stops honoring
// fasthttp defaults), and ReadBufferSize:32768 now lets the SAME header through.
func TestHTTPTransport_ReadBufferSize_Raises431Ceiling(t *testing.T) {
	// A ~9 KiB header — well past fasthttp's 4 KiB default, well under 32 KiB.
	req := "GET /v1/health HTTP/1.1\r\nHost: x\r\nX-Big: " +
		strings.Repeat("A", 9000) + "\r\nConnection: close\r\n\r\n"

	// Control: no ReadBufferSize -> fasthttp's 4 KiB default -> 431.
	ctrl := zip.New(zip.Config{AppName: "ctrl", DisableStartupMessage: true})
	ctrl.Get("/v1/health", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	const ctrlAddr = "127.0.0.1:19701"
	go func() { _ = ctrl.Listen("http://" + ctrlAddr) }()
	defer func() { _ = ctrl.Shutdown() }()
	waitDialable(t, ctrlAddr)
	if code := rawHTTPStatus(t, ctrlAddr, req); code != 431 {
		t.Fatalf("control (default 4 KiB buffer): 9 KiB header -> %d, want 431 (the default cap this fix raises)", code)
	}

	// Fixed: ReadBufferSize 32 KiB -> the SAME 9 KiB header is accepted.
	fixed := zip.New(zip.Config{AppName: "fixed", DisableStartupMessage: true, ReadBufferSize: 32768})
	fixed.Get("/v1/health", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	const fixedAddr = "127.0.0.1:19702"
	go func() { _ = fixed.Listen("http://" + fixedAddr) }()
	defer func() { _ = fixed.Shutdown() }()
	waitDialable(t, fixedAddr)
	if code := rawHTTPStatus(t, fixedAddr, req); code != 200 {
		t.Fatalf("fixed (ReadBufferSize 32 KiB): 9 KiB header -> %d, want 200 (431 means the knob is still dropped at the wire)", code)
	}
}

// rawHTTPStatus opens a raw TCP conn, writes rawRequest verbatim (so the test
// controls exact header bytes, unlike a buffered client), and returns the
// response status code from the status line.
func rawHTTPStatus(t *testing.T, addr, rawRequest string) int {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, rawRequest); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	var proto string
	var code int
	if _, err := fmt.Sscanf(line, "%s %d", &proto, &code); err != nil {
		t.Fatalf("parse status line %q: %v", line, err)
	}
	return code
}

// waitDialable blocks until a plain TCP dial to addr succeeds (the HTTP
// transport is up) or the bound is hit.
func waitDialable(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("http listener %s never became dialable", addr)
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
