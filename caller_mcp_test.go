package zip

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
)

// The identity a request carries must reach the handler over EVERY transport,
// because op.invoke is one handler core and the caller is a property of the
// request, not of the door it came through.
//
// MCP was the one door that did not. typed.go and call.go both built the
// handler's context with callerContext(fc); mcp.go used fc.Context(), which
// carries no reference to the *fasthttp.RequestCtx, so CallerOf() came back
// EMPTY for any op dispatched by a tools/call. Two consequences, and the second
// is the expensive one:
//
//   - an Authorizer reading the caller could not tell who was asking;
//   - an onward zip.Call made from inside such a handler forwarded NO identity
//     headers at all, so the next service downstream saw an anonymous request
//     that had in fact arrived authenticated.
//
// It is also why hanzoai/cloud grew a hand-installed Bridge() middleware at the
// app root in ~100 places: middleware runs on the fiber Ctx, so parking the
// identity there papered over the hole — in that repo's private vocabulary,
// for that repo only.
func TestCallerReachesTheHandlerOverMCP(t *testing.T) {
	type in struct {
		X string `json:"x"`
	}
	type out struct {
		Org  string `json:"org"`
		User string `json:"user"`
	}

	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	Get(app, "/v1/who", func(ctx context.Context, _ *in) (*out, error) {
		c := CallerOf(ctx)
		return &out{Org: c.Org, User: c.User}, nil
	}, WithOperationID("who"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	req := httptest.NewRequest("POST", "/mcp",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"who","arguments":{}}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderOrg, "acme")
	req.Header.Set(HeaderUser, "u-1")

	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	var got struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("reply is not JSON: %v (%s)", err, body)
	}
	if len(got.Result.Content) == 0 {
		t.Fatalf("tools/call returned no content: %s", body)
	}
	var who out
	if err := json.Unmarshal([]byte(got.Result.Content[0].Text), &who); err != nil {
		t.Fatalf("content is not the op's output: %v (%s)", err, got.Result.Content[0].Text)
	}
	if who.Org != "acme" || who.User != "u-1" {
		t.Errorf("CallerOf over MCP = %+v, want org=acme user=u-1 — the tools/call handler "+
			"was built with fc.Context() instead of callerContext(fc)\n  body: %s", who, body)
	}
}
