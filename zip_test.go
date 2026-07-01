package zip

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
)

// call drives the app's fasthttp handler in-process (no network).
func call(a *App, method, path string) (int, string) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(path)
	a.Handler()(ctx)
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

func TestStaticRoute(t *testing.T) {
	a := New()
	a.Fiber().Get("/v1/health", func(c fiber.Ctx) error { return c.SendString("ok") })
	if code, body := call(a, "GET", "/v1/health"); code != 200 || body != "ok" {
		t.Fatalf("static route: got %d %q", code, body)
	}
}

func TestDynamicMountAndUnmount(t *testing.T) {
	a := New()
	a.MountFast("/v1/bc/C", func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(200)
		ctx.SetBodyString("C-chain")
	})
	if code, body := call(a, "POST", "/v1/bc/C/rpc"); code != 200 || body != "C-chain" {
		t.Fatalf("dynamic mount: got %d %q", code, body)
	}
	a.Unmount("/v1/bc/C")
	if code, _ := call(a, "POST", "/v1/bc/C/rpc"); code == 200 {
		t.Fatalf("after unmount expected non-200, got %d", code)
	}
}

func TestNetHTTPMount(t *testing.T) {
	a := New()
	a.Mount("/v1/info", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "info-ok")
	}))
	if code, body := call(a, "GET", "/v1/info"); code != 200 || body != "info-ok" {
		t.Fatalf("net/http mount: got %d %q", code, body)
	}
}

func TestLongestPrefixWins(t *testing.T) {
	a := New()
	a.MountFast("/v1", func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("root") })
	a.MountFast("/v1/bc/C", func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("cchain") })
	if _, body := call(a, "GET", "/v1/bc/C/rpc"); body != "cchain" {
		t.Fatalf("longest-prefix: got %q", body)
	}
	if _, body := call(a, "GET", "/v1/status"); body != "root" {
		t.Fatalf("prefix fallback: got %q", body)
	}
}

// TestZAPPrimaryRoundTrip proves the SAME app served over the ZAP transport
// returns identical results — ZAP is the primary path.
func TestZAPPrimaryRoundTrip(t *testing.T) {
	a := New()
	a.MountFast("/v1/ping", func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("pong") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &zaphttp.Server{Handler: a.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	time.Sleep(50 * time.Millisecond)

	tr := zaphttp.NewTransport(ln.Addr().String())
	defer tr.CloseIdleConnections()
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI("http://zap/v1/ping")
	req.Header.SetMethod("GET")
	if err := tr.Do(req, resp); err != nil {
		t.Fatalf("zap round-trip: %v", err)
	}
	if got := string(resp.Body()); got != "pong" {
		t.Fatalf("zap round-trip: got %q want pong", got)
	}
}
