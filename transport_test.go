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
	"github.com/zap-proto/http"

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

	addr := freeAddr(t)
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

	tr := http.Dial("tcp", addr)
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

	zapAddr := freeAddr(t)
	httpAddr := freeAddr(t)
	go func() { _ = app.Listen(zapAddr, "http://"+httpAddr) }() // ONE call, both transports
	defer func() { _ = app.Shutdown() }()
	waitReachable(t, zapAddr)

	// ZAP side.
	ztr := http.Dial("tcp", zapAddr)
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
	ctrlAddr := freeAddr(t)
	go func() { _ = ctrl.Listen("http://" + ctrlAddr) }()
	defer func() { _ = ctrl.Shutdown() }()
	waitDialable(t, ctrlAddr)
	if code := rawHTTPStatus(t, ctrlAddr, req); code != 431 {
		t.Fatalf("control (default 4 KiB buffer): 9 KiB header -> %d, want 431 (the default cap this fix raises)", code)
	}

	// Fixed: ReadBufferSize 32 KiB -> the SAME 9 KiB header is accepted.
	fixed := zip.New(zip.Config{AppName: "fixed", DisableStartupMessage: true, ReadBufferSize: 32768})
	fixed.Get("/v1/health", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	fixedAddr := freeAddr(t)
	go func() { _ = fixed.Listen("http://" + fixedAddr) }()
	defer func() { _ = fixed.Shutdown() }()
	waitDialable(t, fixedAddr)
	if code := rawHTTPStatus(t, fixedAddr, req); code != 200 {
		t.Fatalf("fixed (ReadBufferSize 32 KiB): 9 KiB header -> %d, want 200 (431 means the knob is still dropped at the wire)", code)
	}
}

// TestHTTPTransport_BodyLimitReachesTheSocket is the regression test for the
// same drop one field over: the HTTP transport built a bare fasthttp.Server and
// honored zip.Config.BodyLimit nowhere, so every body was capped at fasthttp's
// 4 MiB default however the App was configured. fiberConfig sets fiber's own
// BodyLimit, which reaches MaxRequestBodySize only when fiber owns the listener
// — here the transport does, so the two disagreed and the socket won.
//
// It cost a production deployment configured for 100 MiB: 4,194,304 bytes were
// answered and 4,194,305 were refused, which is 4<<20 exactly. Every site
// publish over 4 MiB and every full-context prompt (a 1M-token prompt is ~4.3 MB
// of JSON) died as an opaque 400 "Error when parsing request", a message that
// reads like a malformed payload rather than a size cap.
//
// A REAL socket, both directions. The config-level assertion the cloud carried
// (BodyLimit != 4<<20) passed the whole time this was broken, because the value
// was set correctly and never reached the wire.
func TestHTTPTransport_BodyLimitReachesTheSocket(t *testing.T) {
	post := func(n int) string {
		return fmt.Sprintf("POST /v1/echo HTTP/1.1\r\nHost: x\r\nContent-Type: application/octet-stream\r\n"+
			"Content-Length: %d\r\nConnection: close\r\n\r\n%s", n, strings.Repeat("a", n))
	}
	serve := func(name string, limit int) string {
		app := zip.New(zip.Config{AppName: name, DisableStartupMessage: true, BodyLimit: limit})
		app.Post("/v1/echo", func(c *zip.Ctx) error { return c.JSON(200, map[string]int{"n": len(c.Body())}) })
		addr := freeAddr(t)
		go func() { _ = app.Listen("http://" + addr) }()
		t.Cleanup(func() { _ = app.Shutdown() })
		waitDialable(t, addr)
		return addr
	}

	// Raised: 5 MiB is past fasthttp's 4 MiB default and inside an 8 MiB App.
	// A refusal here means the limit never left the Config. This is the arm that
	// was production's outage.
	if !answered200(t, serve("raised", 8<<20), post(5<<20)) {
		t.Errorf("BodyLimit 8 MiB: 5 MiB body was REFUSED, want 200 (the knob is still dropped at the wire)")
	}

	// Lowered: the knob has to bind in BOTH directions, or a deployment that
	// TIGHTENS the ceiling silently keeps serving 4 MiB. 2 MiB is past a 1 MiB
	// App and inside the default the bug left on the socket, so this arm answers
	// 200 precisely when the transport is ignoring Config.
	//
	// Read as "did the handler run", not as a status: fasthttp refuses an
	// oversized body while the client is still writing it, so what comes back is
	// a connection reset as often as a response. Either is the refusal; only a
	// 200 is the bug.
	if answered200(t, serve("lowered", 1<<20), post(2<<20)) {
		t.Errorf("BodyLimit 1 MiB: 2 MiB body was SERVED, want a refusal (the socket is still on fasthttp's 4 MiB default)")
	}
}

// answered200 reports whether the handler answered 200. A write or read failure is a
// refusal, not a test failure: fasthttp drops the connection mid-body when the
// limit is crossed, so the peer never gets a status line to parse.
func answered200(t *testing.T, addr, rawRequest string) bool {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, rawRequest); err != nil {
		return false
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	return err == nil && strings.HasPrefix(line, "HTTP/1.1 200")
}

// TestHTTPTransport_ServerHeaderCoversPreRoutingErrors proves fasthttp's OWN
// pre-routing error responses (431 on header overflow) carry the App's
// ServerHeader and NEVER the framework default "fasthttp"/"zip". Those bytes are
// written before any fiber middleware runs, so ProductionHeaders cannot brand
// them — the transport must. A REAL socket; Fiber().Test() cannot observe this
// path (which is exactly how the leak hid from the in-memory suite).
func TestHTTPTransport_ServerHeaderCoversPreRoutingErrors(t *testing.T) {
	// 9 KiB header overflows fasthttp's default 4 KiB read buffer -> 431, emitted
	// before routing.
	req := "GET /v1/health HTTP/1.1\r\nHost: api.lux.network\r\nX-Big: " +
		strings.Repeat("A", 9000) + "\r\nConnection: close\r\n\r\n"

	// Branded: the pre-routing 431 carries Server: <brand>, never the framework.
	brand := zip.New(zip.Config{AppName: "brand", DisableStartupMessage: true, ServerHeader: "hanzo"})
	brand.Get("/v1/health", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	brandAddr := freeAddr(t)
	go func() { _ = brand.Listen("http://" + brandAddr) }()
	defer func() { _ = brand.Shutdown() }()
	waitDialable(t, brandAddr)
	resp := rawHTTPResponse(t, brandAddr, req)
	if !strings.HasPrefix(resp, "HTTP/1.1 431") {
		t.Fatalf("want a 431 pre-routing error, got:\n%s", resp)
	}
	low := strings.ToLower(resp)
	for _, bad := range []string{"server: fasthttp", "server: zip", "server: fiber"} {
		if strings.Contains(low, bad) {
			t.Errorf("pre-routing 431 leaked framework name %q:\n%s", bad, resp)
		}
	}
	if !strings.Contains(low, "server: hanzo") {
		t.Errorf("pre-routing 431 missing Server: hanzo:\n%s", resp)
	}

	// Suppressed: ServerHeader "-" emits NO Server header on the pre-routing error.
	quiet := zip.New(zip.Config{AppName: "quiet", DisableStartupMessage: true, ServerHeader: "-"})
	quiet.Get("/v1/health", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	quietAddr := freeAddr(t)
	go func() { _ = quiet.Listen("http://" + quietAddr) }()
	defer func() { _ = quiet.Shutdown() }()
	waitDialable(t, quietAddr)
	if low := strings.ToLower(rawHTTPResponse(t, quietAddr, req)); strings.Contains(low, "server:") {
		t.Errorf(`ServerHeader "-" still emitted a Server header on the pre-routing error:\n%s`, low)
	}
}

// rawHTTPResponse writes rawRequest verbatim and returns the FULL raw response
// (status line + headers + body) so a test can inspect response headers the
// status code alone hides. Relies on Connection: close so the read ends at EOF.
func rawHTTPResponse(t *testing.T, addr, rawRequest string) string {
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
	b, _ := io.ReadAll(c)
	return string(b)
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
	tr := http.Dial("tcp", addr)
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
