package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

type thingIn struct {
	Owner string `json:"owner"`
}
type thingOut struct {
	OK bool `json:"ok"`
}

// effOut is the reply of the op the parity table drives — package-scoped because
// all four projections assert on it.
type effOut struct {
	OK bool `json:"ok"`
}

// The op-invoke authorizer sees the SAME decoded In the handler will run on, for
// REST and MCP alike, and aborts the op before the handler runs when it refuses.
// This is the seam that makes the value authorized equal the value executed —
// there is no second parse of the body to diverge from.
func TestAuthorizer_GatesDecodedInputAcrossRESTandMCP(t *testing.T) {
	app := zip.New(zip.Config{AppName: "authztest", DisableStartupMessage: true})

	var handlerRan bool
	zip.Post(app, "/v1/things", func(_ context.Context, in *thingIn) (*thingOut, error) {
		handlerRan = true
		return &thingOut{OK: true}, nil
	}, zip.WithOperationID("createThing"))

	// The decision keys on the DECODED owner: only "self" passes.
	var seen []string
	app.Authorize(func(_ context.Context, op zip.Op, in any) (zip.Decision, error) {
		ti, ok := in.(*thingIn)
		if !ok {
			t.Fatalf("authorizer got %T, want *thingIn (the decoded input)", in)
		}
		seen = append(seen, op.OperationID+"/"+op.Method+":"+ti.Owner)
		if ti.Owner != "self" {
			return zip.Deny_("owner", "forbidden"), nil
		}
		return zip.Grant(), nil
	})
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		return resp.StatusCode, out
	}

	// REST allow: an authorized value runs the handler.
	handlerRan = false
	if code, _ := post("/v1/things", map[string]any{"owner": "self"}); code != 200 {
		t.Fatalf("REST allow status = %d, want 200", code)
	}
	if !handlerRan {
		t.Fatal("handler must run when the authorizer allows (REST)")
	}

	// REST deny: a refused value is 403 and the handler never runs.
	handlerRan = false
	if code, _ := post("/v1/things", map[string]any{"owner": "attacker"}); code != 403 {
		t.Fatalf("REST deny status = %d, want 403", code)
	}
	if handlerRan {
		t.Fatal("handler must NOT run when the authorizer refuses (REST)")
	}

	// MCP deny: the SAME hook gates tools/call on the decoded arguments — the
	// handler never runs, and the result is an MCP isError (not a transport error).
	handlerRan = false
	_, out := post("/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "createThing", "arguments": map[string]any{"owner": "attacker"}},
	})
	if handlerRan {
		t.Fatal("handler must NOT run when the authorizer refuses (MCP)")
	}
	if result, _ := out["result"].(map[string]any); result == nil || result["isError"] != true {
		t.Fatalf("MCP deny should be an isError result, got %v", out)
	}

	// MCP allow: authorized arguments run the handler over MCP too.
	handlerRan = false
	post("/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "createThing", "arguments": map[string]any{"owner": "self"}},
	})
	if !handlerRan {
		t.Fatal("handler must run when the authorizer allows (MCP)")
	}

	// The authorizer was consulted on the decoded owner for both transports.
	if len(seen) != 4 {
		t.Fatalf("authorizer calls = %v, want 4 (REST allow/deny + MCP deny/allow)", seen)
	}
}

// A nil authorizer leaves the op open — the framework default is unchanged for
// apps that install none.
func TestAuthorizer_NilIsOpen(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	var ran bool
	zip.Post(app, "/v1/open", func(_ context.Context, _ *thingIn) (*thingOut, error) {
		ran = true
		return &thingOut{OK: true}, nil
	})
	b, _ := json.Marshal(map[string]any{"owner": "anyone"})
	req := httptest.NewRequest("POST", "/v1/open", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !ran {
		t.Fatalf("nil authorizer must leave the op open: status=%d ran=%v", resp.StatusCode, ran)
	}
}

// The three-valued Decision renders the SAME outcome on ALL FOUR projections:
// allow runs the handler (REST 200, MCP content, a value in-process and over the
// call plane), deny refuses without running it (REST 403 carrying Rule/Reason, MCP
// isError, an *HTTPError to a Go caller), approve holds it without running it
// (REST and the call plane 202 {status:"held"}, MCP a held content that is NOT an
// error, and an *Approval on the error lane in Go, read by zip.HeldOf). One seam
// decides; the projections only render — so a table over the three effects proves
// all four agree.
//
// The two Go projections are in the table because they are where a held op used to
// be unreadable: [Here] reported it as a type mistake about *Approval, and the
// call plane sent it under a 200 that [Call] decoded into Out — a held op arriving
// as a finished one carrying a fabricated value.
func TestDecision_ProjectionParity(t *testing.T) {
	cases := []struct {
		name     string
		owner    string // drives the decision below
		restCode int    // REST status the effect renders
		ranWant  bool   // whether the handler must have run
		checkMCP func(*testing.T, map[string]any)
	}{
		{
			name: "allow", owner: "self", restCode: 200, ranWant: true,
			checkMCP: func(t *testing.T, result map[string]any) {
				if result["isError"] == true {
					t.Fatalf("allow must not be an MCP error: %v", result)
				}
				if got := mcpText(t, result); !strings.Contains(got, `"ok":true`) {
					t.Fatalf("allow MCP content = %q, want the handler's output", got)
				}
			},
		},
		{
			name: "deny", owner: "attacker", restCode: 403, ranWant: false,
			checkMCP: func(t *testing.T, result map[string]any) {
				if result["isError"] != true {
					t.Fatalf("deny must be an MCP isError: %v", result)
				}
				if got := mcpText(t, result); !strings.Contains(got, "not owner") {
					t.Fatalf("deny MCP content = %q, want the reason", got)
				}
			},
		},
		{
			name: "approve", owner: "review", restCode: 202, ranWant: false,
			checkMCP: func(t *testing.T, result map[string]any) {
				if result["isError"] == true {
					t.Fatalf("approve must NOT be an MCP error — a held op is not a failure: %v", result)
				}
				if got := mcpText(t, result); !strings.Contains(got, `"status":"held"`) {
					t.Fatalf("approve MCP content = %q, want a held body", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := zip.New(zip.Config{AppName: "parity", DisableStartupMessage: true})
			var ran bool
			zip.Post(app, "/v1/act", func(_ context.Context, _ *thingIn) (*effOut, error) {
				ran = true
				return &effOut{OK: true}, nil
			}, zip.WithOperationID("act"))
			// One hook, three answers keyed on the decoded owner.
			app.Authorize(func(_ context.Context, _ zip.Op, in any) (zip.Decision, error) {
				switch in.(*thingIn).Owner {
				case "self":
					return zip.Grant(), nil
				case "review":
					return zip.Approve_("kit", "needs sign-off"), nil
				default:
					return zip.Deny_("owner", "not owner"), nil
				}
			})
			if err := app.Build(); err != nil {
				t.Fatalf("Build: %v", err)
			}

			// REST projection.
			ran = false
			body, _ := json.Marshal(map[string]any{"owner": tc.owner})
			req := httptest.NewRequest("POST", "/v1/act", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Fiber().Test(req)
			if err != nil {
				t.Fatalf("REST: %v", err)
			}
			var restBody map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&restBody)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.restCode {
				t.Fatalf("REST %s status = %d, want %d", tc.name, resp.StatusCode, tc.restCode)
			}
			if ran != tc.ranWant {
				t.Fatalf("REST %s handler ran = %v, want %v", tc.name, ran, tc.ranWant)
			}
			switch tc.name {
			case "deny":
				if restBody["status"] != float64(403) || restBody["code"] != "owner" || restBody["error"] != "not owner" {
					t.Fatalf("REST deny body must carry Rule/Reason: %v", restBody)
				}
			case "approve":
				if restBody["status"] != "held" {
					t.Fatalf("REST approve body must be held: %v", restBody)
				}
				if restBody["rule"] != "kit" || restBody["reason"] != "needs sign-off" {
					t.Fatalf("REST approve body must carry the approval: %v", restBody)
				}
			}

			// MCP projection — same hook, same three outcomes.
			ran = false
			out := mcpCall(t, app, "act", map[string]any{"owner": tc.owner})
			result, _ := out["result"].(map[string]any)
			if result == nil {
				t.Fatalf("MCP %s: no result in %v", tc.name, out)
			}
			if ran != tc.ranWant {
				t.Fatalf("MCP %s handler ran = %v, want %v", tc.name, ran, tc.ranWant)
			}
			tc.checkMCP(t, result)

			// IN-PROCESS projection — no wire under it, same three outcomes.
			ran = false
			local, err := zip.Here[thingIn, effOut](context.Background(), app, "act", &thingIn{Owner: tc.owner})
			if ran != tc.ranWant {
				t.Fatalf("Here %s handler ran = %v, want %v", tc.name, ran, tc.ranWant)
			}
			checkGo(t, tc.name, local, err)

			// CALL PLANE — the same op over a real socket, addressed by name.
			t.Setenv(zip.RuntimeDirEnv, sockDir(t))
			sock := zip.SocketPath("parity")
			go func() { _ = app.Listen(sock) }()
			waitFor(t, sock)
			t.Cleanup(func() { _ = app.Shutdown() })
			conn, derr := zip.DialApp("parity")
			if derr != nil {
				t.Fatalf("dial: %v", derr)
			}
			defer func() { _ = conn.Close() }()
			ran = false
			remote, err := zip.Call[thingIn, effOut](context.Background(), conn, "act", &thingIn{Owner: tc.owner})
			if ran != tc.ranWant {
				t.Fatalf("Call %s handler ran = %v, want %v", tc.name, ran, tc.ranWant)
			}
			checkGo(t, tc.name, remote, err)
		})
	}
}

// checkGo asserts the (value, error) pair a Go caller gets — in-process or over
// the call plane — carries the effect and nothing else. A held op has NO value:
// the whole point is that a caller cannot read one out of it.
func checkGo(t *testing.T, effect string, out *effOut, err error) {
	t.Helper()
	a, held := zip.HeldOf(err)
	switch effect {
	case "allow":
		if err != nil || out == nil || !out.OK {
			t.Fatalf("allow: got (%+v, %v), want the handler's value", out, err)
		}
	case "deny":
		if held {
			t.Fatalf("deny came back as a held op: %+v", a)
		}
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != 403 || he.Code != "owner" || he.Msg != "not owner" {
			t.Fatalf("deny: err = %v, want a 403 carrying Rule/Reason", err)
		}
		if out != nil {
			t.Fatalf("deny returned a value: %+v", out)
		}
	case "approve":
		if !held {
			t.Fatalf("approve: err = %v (%T), want a *zip.Approval readable by zip.HeldOf", err, err)
		}
		if out != nil {
			t.Fatalf("APPROVE RETURNED A VALUE (%+v) — a held op has no reply, and reading one is reading a fabrication", out)
		}
		if a.Status != zip.Held || a.Rule != "kit" || a.Reason != "needs sign-off" {
			t.Fatalf("approval = %+v, want {held, kit, needs sign-off}", a)
		}
	}
}

// Decide bridges a pre-Decision check: a nil error grants, an *HTTPError below
// 500 denies with its code/message, and a 5xx passes through as a genuine
// internal failure. It keeps a gate written against the old error hook working
// by wrapping it once.
func TestDecide_ErrorAdapter(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil grants", nil, 200},
		{"forbidden denies", zip.ErrForbidden("nope"), 403},
		{"internal passes through", zip.ErrInternal("boom"), 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := zip.New(zip.Config{AppName: "adapt", DisableStartupMessage: true})
			zip.Post(app, "/v1/x", func(_ context.Context, _ *thingIn) (*thingOut, error) {
				return &thingOut{OK: true}, nil
			})
			app.Authorize(zip.Decide(func(context.Context, zip.Op, any) error { return tc.err }))
			if err := app.Build(); err != nil {
				t.Fatalf("Build: %v", err)
			}
			b, _ := json.Marshal(map[string]any{"owner": "x"})
			req := httptest.NewRequest("POST", "/v1/x", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Fiber().Test(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("Decide(%v) status = %d, want %d", tc.err, resp.StatusCode, tc.want)
			}
		})
	}
}

// A gated service publishes the 202 held body beside every op's success response,
// so a generated SDK reads Result<T> = Done<T> | Approval. An ungated service
// never holds, so its document says nothing about 202.
func TestDecision_OpenAPIPublishesTheHeldBody(t *testing.T) {
	build := func(gated bool) map[string]any {
		app := zip.New(zip.Config{AppName: "doc", DisableStartupMessage: true})
		zip.Post(app, "/v1/act", func(_ context.Context, _ *thingIn) (*thingOut, error) {
			return &thingOut{OK: true}, nil
		}, zip.WithOperationID("act"))
		if gated {
			app.Authorize(func(context.Context, zip.Op, any) (zip.Decision, error) { return zip.Grant(), nil })
		}
		if err := app.Build(); err != nil {
			t.Fatalf("Build: %v", err)
		}
		return app.OpenAPISpec()
	}
	responsesOf := func(spec map[string]any) map[string]any {
		paths := spec["paths"].(map[string]map[string]any)
		post := paths["/v1/act"]["post"].(map[string]any)
		return post["responses"].(map[string]any)
	}

	if _, ok := responsesOf(build(true))["202"]; !ok {
		t.Fatal("gated service must publish a 202 held response")
	}
	if _, ok := responsesOf(build(false))["202"]; ok {
		t.Fatal("ungated service must not publish a 202 held response")
	}
}

// mcpCall runs one tools/call over the app's /mcp route and returns the decoded
// JSON-RPC envelope.
func mcpCall(t *testing.T, app *zip.App, tool string, args map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("MCP: %v", err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	return out
}

// mcpText pulls the first text content out of one MCP tool result.
func mcpText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no MCP content in %v", result)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}
