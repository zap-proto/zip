package zip

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/valyala/fasthttp"
)

// sentTrace is the traceparent forwardIdentity writes onto an outbound request.
func sentTrace(ctx context.Context) string {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	forwardIdentity(ctx, req)
	return string(req.Header.Peek(HeaderTrace))
}

// A host's own trace position rides a call that has no request behind it, wins
// over the request's when there is one, and an empty answer leaves the request's.
func TestTraceparentHook(t *testing.T) {
	const own = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	old := Traceparent
	t.Cleanup(func() { Traceparent = old })

	Traceparent = nil
	if got := sentTrace(context.Background()); got != "" {
		t.Fatalf("no hook, no request: sent %q, want nothing", got)
	}

	Traceparent = func(context.Context) string { return own }
	if got := sentTrace(context.Background()); got != own {
		t.Fatalf("detached call: sent %q, want %q", got, own)
	}

	var fromRequest, overridden string
	app := New(Config{AppName: "front", DisableStartupMessage: true})
	app.Raw("GET", "/ask", func(c *Ctx) error {
		Traceparent = func(context.Context) string { return "" }
		fromRequest = sentTrace(c.Forward())
		Traceparent = func(context.Context) string { return own }
		overridden = sentTrace(c.Forward())
		return c.JSON(200, map[string]string{"ok": "1"})
	})
	resp, err := app.Fiber().Test(httptest.NewRequest("GET", "/ask", nil))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if fromRequest == "" || fromRequest == own {
		t.Fatalf("empty answer: sent %q, want the request's own traceparent", fromRequest)
	}
	if overridden != own {
		t.Fatalf("hook answer: sent %q, want %q over the request's", overridden, own)
	}
}
