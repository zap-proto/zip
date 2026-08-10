package zip

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	zapmcp "github.com/zap-proto/mcp"
)

// The point of the whole inversion, stated as a test: an agent reaches this
// app's tools over ZAP with NO HTTP LISTENER RUNNING — no Listen, no fiber
// router, no request, no status code.
//
// Before, [App.MCP] was a fiber handler (mcpCall(fc fiber.Ctx, …)), so a
// tools/call could not exist until an HTTP request did. That put the transport
// UNDERNEATH the protocol: MCP was something HTTP carried rather than something
// ZAP speaks. The assertions below are deliberately about what is NOT running —
// a test that only checked the answers would pass just as happily with an HTTP
// server quietly behind it.

// mcpApp is an app with two typed ops and nothing else.
func mcpApp(t *testing.T) *App {
	t.Helper()
	app := quiet("tools")
	Get(app, "/v1/answer", func(ctx context.Context, in *struct {
		Q string `json:"q"`
	}) (*struct {
		A string `json:"a"`
	}, error) {
		return &struct {
			A string `json:"a"`
		}{A: "42:" + in.Q}, nil
	}, WithOperationID("answer"))
	Post(app, "/v1/refuse", func(ctx context.Context, in *struct{}) (*struct{}, error) {
		return nil, ErrForbidden("not for you")
	}, WithOperationID("refuse"))
	return app
}

// zapDoor serves app.MCP over ZAP on a unix socket and returns a client. It
// never calls Listen, so nothing in this path builds a router or binds an HTTP
// port.
func zapDoor(t *testing.T, app *App) *zapmcp.Transport {
	t.Helper()
	sock := filepath.Join(sockDir(t), "mcp.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &zapmcp.Server{Handler: app.MCP}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })

	c := zapmcp.Dial("unix", sock)
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func TestMCP_ServesOverZapWithNoHTTPListener(t *testing.T) {
	app := mcpApp(t)
	c := zapDoor(t, app)
	ctx := context.Background()

	// ---- the app is not serving HTTP, and never was ----------------------
	if app.prepared.Load() {
		t.Fatal("the fiber router was built — this app was supposed to serve only over ZAP")
	}
	app.srvMu.Lock()
	servers := len(app.servers)
	app.srvMu.Unlock()
	if servers != 0 {
		t.Fatalf("%d transport listener(s) running; want 0", servers)
	}
	if n := app.listening.Load(); n != 0 {
		t.Fatalf("listening count = %d; want 0", n)
	}

	// ---- and yet it is a complete MCP server -----------------------------
	out, err := c.Call(ctx, "initialize", nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	var hello struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(out, &hello); err != nil {
		t.Fatalf("initialize result: %v (%s)", err, out)
	}
	if hello.ProtocolVersion != mcpProtocolVersion || hello.ServerInfo.Name != "tools" {
		t.Fatalf("initialize = %s", out)
	}

	// tools/list must carry the app's REAL ops. An empty array here is the
	// failure mode that looks like success: the door answers, and says nothing.
	if out, err = c.Call(ctx, "tools/list", nil); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var listed struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("tools/list result: %v (%s)", err, out)
	}
	got := map[string]bool{}
	for _, tool := range listed.Tools {
		got[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no inputSchema", tool.Name)
		}
	}
	if !got["answer"] || !got["refuse"] {
		t.Fatalf("tools/list = %s, want both declared ops", out)
	}

	// tools/call runs the SAME handler core a REST request would.
	if out, err = c.Call(ctx, "tools/call",
		json.RawMessage(`{"name":"answer","arguments":{"q":"life"}}`)); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if text := contentText(t, out); text != `{"a":"42:life"}` {
		t.Fatalf("tools/call content = %q", text)
	}

	// A handler refusal is isError content, not a transport error, per the spec.
	if out, err = c.Call(ctx, "tools/call", json.RawMessage(`{"name":"refuse","arguments":{}}`)); err != nil {
		t.Fatalf("a refused tool answered a transport error: %v", err)
	}
	if !strings.Contains(string(out), `"isError":true`) || !strings.Contains(string(out), "not for you") {
		t.Fatalf("refusal = %s, want isError content carrying the handler's message", out)
	}

	// An unknown tool is a JSON-RPC refusal the agent can read.
	if _, err = c.Call(ctx, "tools/call", json.RawMessage(`{"name":"nope","arguments":{}}`)); err == nil {
		t.Fatal("an unknown tool succeeded")
	} else {
		var e *zapmcp.Error
		if !errorAs(err, &e) || e.Code != zapmcp.CodeParams {
			t.Fatalf("unknown tool = %v, want a -32602", err)
		}
	}

	// Still nothing serving HTTP after all of that.
	if app.prepared.Load() {
		t.Fatal("answering MCP built the fiber router")
	}
}

// The caller a typed op sees over ZAP is the one the CONTEXT carries, because a
// frame has no headers to read one out of. This is what makes org scoping work
// on the ZAP door at all.
func TestMCP_ZapCallerComesFromTheContext(t *testing.T) {
	seen := make(chan Caller, 1)
	app := quiet("tools")
	Get(app, "/v1/whoami", func(ctx context.Context, in *struct{}) (*struct {
		Org string `json:"org"`
	}, error) {
		seen <- CallerOf(ctx)
		return &struct {
			Org string `json:"org"`
		}{Org: CallerOf(ctx).Org}, nil
	}, WithOperationID("whoami"))

	sock := filepath.Join(sockDir(t), "mcp.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// The server states the caller for every frame on the connection — which is
	// where a deployment reads the peer credential and decides who this is.
	srv := &zapmcp.Server{Handler: func(ctx context.Context, f *zapmcp.Frame) *zapmcp.Frame {
		return app.MCP(WithCaller(ctx, Caller{Org: "acme", User: "z"}), f)
	}}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })

	c := zapmcp.Dial("unix", sock)
	t.Cleanup(c.CloseIdleConnections)

	out, err := c.Call(context.Background(), "tools/call",
		json.RawMessage(`{"name":"whoami","arguments":{}}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if text := contentText(t, out); text != `{"org":"acme"}` {
		t.Fatalf("content = %q, want the ctx's org", text)
	}
	if who := <-seen; who.Org != "acme" || who.User != "z" {
		t.Fatalf("handler saw %+v", who)
	}
}

// The two doors are one door. Whatever the HTTP route answers, the ZAP socket
// answers identically — that is what "HTTP is an adapter" has to mean, and the
// only way to be sure is to ask both and compare the bytes.
func TestMCP_BothDoorsAnswerTheSame(t *testing.T) {
	app := mcpApp(t)
	c := zapDoor(t, app)

	over := func(method, params string) string {
		t.Helper()
		var raw json.RawMessage
		if params != "" {
			raw = json.RawMessage(params)
		}
		out, err := c.Call(context.Background(), method, raw)
		if err != nil {
			t.Fatalf("zap %s: %v", method, err)
		}
		return string(out)
	}

	// The HTTP door, on a SECOND app declared identically, so neither door can
	// be answering out of state the other one warmed.
	edge := mcpApp(t)
	body := func(method, params string) string {
		t.Helper()
		msg := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"`
		if params != "" {
			msg += `,"params":` + params
		}
		msg += `}`
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(msg))
		req.Header.Set("Content-Type", mimeJSON)
		resp, err := edge.Test(req)
		if err != nil {
			t.Fatalf("http %s: %v", method, err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("http %s: %v", method, err)
		}
		var env struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("http %s: %v (%s)", method, err, raw)
		}
		return string(env.Result)
	}

	for _, tc := range []struct{ method, params string }{
		{"tools/list", ""},
		{"ping", ""},
		{"tools/call", `{"name":"answer","arguments":{"q":"x"}}`},
		{"tools/call", `{"name":"refuse","arguments":{}}`},
	} {
		z, h := over(tc.method, tc.params), body(tc.method, tc.params)
		if z != h {
			t.Errorf("%s: the doors disagree\n zap: %s\nhttp: %s", tc.method, z, h)
		}
	}
}

// errorAs is errors.As, named here so this file does not import errors for one
// call and shadow the package's own vocabulary.
func errorAs(err error, target **zapmcp.Error) bool {
	e, ok := err.(*zapmcp.Error)
	if ok {
		*target = e
	}
	return ok
}

// contentText lifts the single text content out of an MCP tool result.
func contentText(t *testing.T, result []byte) string {
	t.Helper()
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		t.Fatalf("tool result: %v (%s)", err, result)
	}
	if len(r.Content) != 1 {
		t.Fatalf("tool result carries %d content items: %s", len(r.Content), result)
	}
	return r.Content[0].Text
}
