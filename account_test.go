package zip

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/valyala/fasthttp"
)

// The billing-account claim is identity: it names which account inside the
// principal's org pays. These pin that it crosses a call exactly as the other
// identity fields do, and that a credential with no claim sends none.

// headers() and CallerOf are the two halves of the wire form. Every field a
// caller states must read back unchanged on the far side, the claim included.
func TestCaller_AccountRoundTrips(t *testing.T) {
	want := Caller{
		Org: "acme", Project: "proj-1", User: "u-7", Name: "bob",
		Email: "bob@acme.test", Owner: "acme", Admin: true, OrgAdmin: true,
		RequestID: "req-1", ActedBy: "u-admin", Account: "person:acme/bob",
	}
	h := want.headers()
	if h[HeaderAccount] != "person:acme/bob" {
		t.Fatalf("headers()[%s] = %q, want person:acme/bob", HeaderAccount, h[HeaderAccount])
	}

	app := New(Config{AppName: "who", DisableStartupMessage: true})
	type none struct{}
	app.Get("/v1/who", func(ctx context.Context, _ *none) (*Caller, error) {
		c := CallerOf(ctx)
		c.IP = ""
		return &c, nil
	}, WithOperationID("who"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	req := httptest.NewRequest("GET", "/v1/who", nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var got Caller
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if got != want {
		t.Fatalf("the caller did not round-trip:\n got %+v\nwant %+v", got, want)
	}
}

// forwarded is what forwardIdentity writes onto an outbound request for a ctx,
// as header name → value, with an absent header absent from the map.
func forwarded(ctx context.Context) map[string]string {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	forwardIdentity(ctx, req)
	out := map[string]string{}
	for _, k := range identityHeaders {
		if v := req.Header.Peek(k); v != nil {
			out[k] = string(v)
		}
	}
	return out
}

// forwardedFrom serves one request carrying hdr and answers what forwardIdentity
// wrote for the request-backed context that request's handler holds.
func forwardedFrom(t *testing.T, hdr map[string]string) map[string]string {
	t.Helper()
	var out map[string]string
	app := New(Config{AppName: "front", DisableStartupMessage: true})
	app.Raw("GET", "/ask", func(c *Ctx) error {
		out = forwarded(c.Forward())
		return c.JSON(200, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest("GET", "/ask", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	return out
}

// A request's claim is forwarded with the rest of its identity, and a stated
// caller's claim is sent the same way, so a callee resolving the payer reads the
// account the gateway minted whichever shape the call was made in.
func TestForwardIdentity_CarriesTheAccount(t *testing.T) {
	got := forwardedFrom(t, map[string]string{
		HeaderOrg: "acme", HeaderUser: "u-7", HeaderUserName: "bob",
		HeaderAccount: "person:acme/bob",
	})
	if got[HeaderAccount] != "person:acme/bob" {
		t.Errorf("a request's claim forwarded as %q, want person:acme/bob (%v)", got[HeaderAccount], got)
	}

	stated := WithCaller(context.Background(), Caller{Org: "acme", User: "u-7", Account: "person:acme/bob"})
	if got := forwarded(stated); got[HeaderAccount] != "person:acme/bob" {
		t.Errorf("a stated caller's claim forwarded as %q, want person:acme/bob (%v)", got[HeaderAccount], got)
	}
	if CallerOf(stated).Account != "person:acme/bob" {
		t.Errorf("a stated caller reads back account %q, want person:acme/bob", CallerOf(stated).Account)
	}
}

// No claim, no header. A credential minted without a billing account must reach
// the callee as one with no claim, never as one claiming the empty account.
func TestForwardIdentity_NoClaimSendsNoAccount(t *testing.T) {
	got := forwardedFrom(t, map[string]string{HeaderOrg: "acme", HeaderUser: "u-7", HeaderUserName: "bob"})
	if v, ok := got[HeaderAccount]; ok {
		t.Errorf("a request with no claim forwarded %s = %q, want the header absent", HeaderAccount, v)
	}
	if got[HeaderUser] != "u-7" {
		t.Fatalf("the identity itself did not forward: %v", got)
	}

	stated := WithCaller(context.Background(), Caller{Org: "acme", User: "u-7"})
	if v, ok := forwarded(stated)[HeaderAccount]; ok {
		t.Errorf("a stated caller with no claim forwarded %s = %q, want the header absent", HeaderAccount, v)
	}
	if _, ok := (Caller{Org: "acme"}).headers()[HeaderAccount]; ok {
		t.Errorf("headers() rendered %s for a caller with no claim", HeaderAccount)
	}
}
