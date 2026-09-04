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

// actOut is the reply of the op the parity table drives. Package-scoped because
// every projection asserts on it.
type actOut struct {
	OK bool `json:"ok"`
}

// The three-valued Decision renders the same outcome on EVERY projection.
//
// Allow runs the handler. Deny refuses without running it, carrying the clause
// and the reason. Approve holds it without running it, and what comes back says
// so in the one shape [zip.Approval] defines. One seam decides — the run()
// contract both op.invoke and op.direct funnel through — and the projections
// only render, so a table over the three effects is a proof that all six agree.
//
// The two Go projections earn their place because they are where a held op is
// easiest to lose: [zip.Here] would report it as the caller's mistake about a
// type, and the call plane would send it under a 200 that [zip.Call] decodes
// into Out — a held op arriving as a finished one carrying a value the handler
// never produced.
func TestDecision_ProjectionParity(t *testing.T) {
	cases := []struct {
		name     string
		owner    string // drives the decision below
		restCode int    // the status REST renders for this effect
		ran      bool   // whether the handler must have run
		mcp      func(*testing.T, map[string]any)
	}{
		{
			name: "allow", owner: "self", restCode: 200, ran: true,
			mcp: func(t *testing.T, result map[string]any) {
				if result["isError"] == true {
					t.Fatalf("allow must not be an MCP error: %v", result)
				}
				if got := mcpText(t, result); !strings.Contains(got, `"ok":true`) {
					t.Fatalf("allow MCP content = %q, want the handler's output", got)
				}
			},
		},
		{
			name: "deny", owner: "attacker", restCode: 403, ran: false,
			mcp: func(t *testing.T, result map[string]any) {
				if result["isError"] != true {
					t.Fatalf("deny must be an MCP isError: %v", result)
				}
				if got := mcpText(t, result); !strings.Contains(got, "not the owner") {
					t.Fatalf("deny MCP content = %q, want the reason", got)
				}
			},
		},
		{
			name: "approve", owner: "review", restCode: 202, ran: false,
			mcp: func(t *testing.T, result map[string]any) {
				if result["isError"] == true {
					t.Fatalf("a held op is not a failure and must not be an MCP error: %v", result)
				}
				if got := mcpText(t, result); !strings.Contains(got, `"status":"held"`) {
					t.Fatalf("approve MCP content = %q, want a held body", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			app := zip.New(zip.Config{AppName: "parity", DisableStartupMessage: true})
			zip.Post(app, "/v1/act", func(_ context.Context, _ *thingIn) (*actOut, error) {
				ran = true
				return &actOut{OK: true}, nil
			}, zip.WithOperationID("act"))
			// ONE rule, three answers, keyed on the decoded owner.
			app.Authorize(func(_ context.Context, _ zip.Op, in any) (zip.Decision, error) {
				switch in.(*thingIn).Owner {
				case "self":
					return zip.Decision{Effect: zip.Allow}, nil
				case "review":
					return zip.Decision{Effect: zip.Approve, Clause: "kit", Reason: "needs sign-off"}, nil
				default:
					return zip.Decision{Effect: zip.Deny, Clause: "owner", Reason: "not the owner"}, nil
				}
			})
			if err := app.Build(); err != nil {
				t.Fatalf("Build: %v", err)
			}

			// REST.
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
			if ran != tc.ran {
				t.Fatalf("REST %s handler ran = %v, want %v", tc.name, ran, tc.ran)
			}
			switch tc.name {
			case "deny":
				if restBody["status"] != float64(403) || restBody["code"] != "owner" || restBody["detail"] != "not the owner" {
					t.Fatalf("REST deny body must carry the clause and the reason: %v", restBody)
				}
			case "approve":
				if restBody["status"] != zip.Held || restBody["clause"] != "kit" || restBody["reason"] != "needs sign-off" {
					t.Fatalf("REST approve body must be the approval: %v", restBody)
				}
			}

			// MCP — same rule, same three outcomes, no route underneath.
			ran = false
			out := mcpCall(t, app, "act", map[string]any{"owner": tc.owner})
			result, _ := out["result"].(map[string]any)
			if result == nil {
				t.Fatalf("MCP %s: no result in %v", tc.name, out)
			}
			if ran != tc.ran {
				t.Fatalf("MCP %s handler ran = %v, want %v", tc.name, ran, tc.ran)
			}
			tc.mcp(t, result)

			// In-process — no wire at all.
			ran = false
			local, err := zip.Here[thingIn, actOut](context.Background(), app, "act", &thingIn{Owner: tc.owner})
			if ran != tc.ran {
				t.Fatalf("Here %s handler ran = %v, want %v", tc.name, ran, tc.ran)
			}
			inGo(t, tc.name, local, err)

			// The graph, which has only two lanes — data and errors — so a held
			// op is reported beside the field rather than projected as a value.
			ran = false
			g := app.GraphQL(context.Background(), zip.GraphRequest{
				Query:     `mutation ($o: String) { act(owner: $o) { ok } }`,
				Variables: map[string]any{"o": tc.owner},
			})
			if ran != tc.ran {
				t.Fatalf("graph %s handler ran = %v, want %v", tc.name, ran, tc.ran)
			}
			field, _ := g.Data.(map[string]any)["act"].(map[string]any)
			switch tc.name {
			case "allow":
				if len(g.Errors) != 0 || field["ok"] != true {
					t.Fatalf("graph allow = %+v errs=%v, want the handler's field", field, g.Errors)
				}
			case "deny":
				if field != nil || !graphSays(g, "not the owner") {
					t.Fatalf("graph deny = %+v errs=%v, want the reason and no data", field, g.Errors)
				}
			case "approve":
				if field != nil || !graphSays(g, "held for approval: kit") {
					t.Fatalf("graph approve = %+v errs=%v, want the approval and no data", field, g.Errors)
				}
			}

			// The CLI, run in-process through the same seam. It prints what came
			// back, so a held op prints the approval and not a fabricated reply.
			ran = false
			cmd := commandFor(t, app, "act")
			var printed bytes.Buffer
			cli := app.CLI()
			cli.Out = &printed
			err = cli.Run(context.Background(), []string{cmd.Service, cmd.Name, "--owner", tc.owner})
			if ran != tc.ran {
				t.Fatalf("CLI %s handler ran = %v, want %v", tc.name, ran, tc.ran)
			}
			switch tc.name {
			case "allow":
				if err != nil || !strings.Contains(printed.String(), `"ok": true`) {
					t.Fatalf("CLI allow printed %q err=%v, want the handler's output", printed.String(), err)
				}
			case "deny":
				if err == nil || !strings.Contains(err.Error(), "not the owner") {
					t.Fatalf("CLI deny err = %v, want the reason", err)
				}
			case "approve":
				if err != nil {
					t.Fatalf("CLI approve err = %v — a held op is not a failure", err)
				}
				if !strings.Contains(printed.String(), `"status": "held"`) ||
					!strings.Contains(printed.String(), `"clause": "kit"`) {
					t.Fatalf("CLI approve printed %q, want the approval", printed.String())
				}
			}

			// The call plane — the same op over a real socket, addressed by name.
			t.Setenv(zip.RuntimeDirEnv, sockDir(t))
			sock := zip.SocketPath("parity")
			go func() { _ = app.Listen(zip.Addr(sock)) }()
			t.Cleanup(func() { _ = app.Shutdown() })
			waitFor(t, sock)
			conn, derr := zip.DialApp("parity")
			if derr != nil {
				t.Fatalf("dial: %v", derr)
			}
			defer func() { _ = conn.Close() }()
			ran = false
			remote, err := zip.Call[thingIn, actOut](context.Background(), conn, "act", &thingIn{Owner: tc.owner})
			if ran != tc.ran {
				t.Fatalf("Call %s handler ran = %v, want %v", tc.name, ran, tc.ran)
			}
			inGo(t, tc.name, remote, err)
		})
	}
}

// inGo asserts the (value, error) pair a Go caller gets — in-process or over the
// call plane — carries the effect and nothing else. A held op has NO value: the
// whole point is that a caller cannot read one out of it.
func inGo(t *testing.T, effect string, out *actOut, err error) {
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
		if !errors.As(err, &he) || he.Status != 403 || he.Code != "owner" || he.Msg != "not the owner" {
			t.Fatalf("deny: err = %v, want a 403 carrying the clause and the reason", err)
		}
		if out != nil {
			t.Fatalf("deny returned a value: %+v", out)
		}
	case "approve":
		if !held {
			t.Fatalf("approve: err = %v (%T), want a *zip.Approval readable by zip.HeldOf", err, err)
		}
		if out != nil {
			t.Fatalf("approve returned the value %+v — a held op has no reply, and reading one reads a fabrication", out)
		}
		if a.Status != zip.Held || a.Clause != "kit" || a.Reason != "needs sign-off" {
			t.Fatalf("approval = %+v, want {held, kit, needs sign-off}", a)
		}
	}
}

// A rule that fails is not a rule that refuses. An authorizer whose authority is
// unreachable answers on the error lane, and that error is the response — a 503
// rather than the 403 a Deny renders, because "I could not decide" and "I
// decided no" are different answers and a client retries only one of them.
func TestDecision_AFailedCheckIsNotARefusal(t *testing.T) {
	app := zip.New(zip.Config{AppName: "broken", DisableStartupMessage: true})
	zip.Post(app, "/v1/act", func(_ context.Context, _ *thingIn) (*actOut, error) {
		return &actOut{OK: true}, nil
	}, zip.WithOperationID("act"))
	app.Authorize(func(context.Context, zip.Op, any) (zip.Decision, error) {
		return zip.Decision{}, zip.Errorf(503, "the authority is unreachable")
	})
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	req := httptest.NewRequest("POST", "/v1/act", strings.NewReader(`{"owner":"self"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d, want 503 — a check that failed is not a refusal", resp.StatusCode)
	}
}

// A gated service publishes the held body beside every op's success response, so
// a generated client reads the pair instead of mistaking a held op for a
// finished one. An op under no rule never holds, so its document says nothing
// about it.
func TestDecision_TheDocumentPublishesTheHeldBody(t *testing.T) {
	responses := func(gated bool) map[string]any {
		app := zip.New(zip.Config{AppName: "doc", DisableStartupMessage: true})
		zip.Post(app, "/v1/act", func(_ context.Context, _ *thingIn) (*thingOut, error) {
			return &thingOut{OK: true}, nil
		}, zip.WithOperationID("act"))
		if gated {
			app.Authorize(func(context.Context, zip.Op, any) (zip.Decision, error) {
				return zip.Decision{Effect: zip.Allow}, nil
			})
		}
		if err := app.Build(); err != nil {
			t.Fatalf("Build: %v", err)
		}
		paths := app.OpenAPISpec()["paths"].(map[string]map[string]any)
		return paths["/v1/act"]["post"].(map[string]any)["responses"].(map[string]any)
	}

	held, ok := responses(true)["202"].(map[string]any)
	if !ok {
		t.Fatal("a gated op must publish the held response")
	}
	schema, _ := held["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if schema == nil {
		t.Fatalf("the held response must publish a schema: %v", held)
	}
	if _, ok := responses(false)["202"]; ok {
		t.Fatal("an op under no rule must not publish the held response")
	}
}

// A HOST'S RULE REACHES A COMPOSED CHILD, so the host's document must say the
// child's ops can hold. The rule is settled at build (see adopt), which is after
// the child registered its ops and before the document renders — so the document
// asks the same question the invoke seam asks, and cannot publish a contract the
// seam does not keep.
func TestDecision_TheDocumentFollowsTheAdoptedRule(t *testing.T) {
	child := zip.New(zip.Config{AppName: "child", DisableStartupMessage: true})
	zip.Post(child, "/v1/child/act", func(_ context.Context, _ *thingIn) (*thingOut, error) {
		return &thingOut{OK: true}, nil
	}, zip.WithOperationID("childAct"))

	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	host.Authorize(func(context.Context, zip.Op, any) (zip.Decision, error) {
		return zip.Decision{Effect: zip.Approve, Clause: "review", Reason: "sign it off"}, nil
	})
	host.Use(child)
	if err := host.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	paths := host.OpenAPISpec()["paths"].(map[string]map[string]any)
	resp := paths["/v1/child/act"]["post"].(map[string]any)["responses"].(map[string]any)
	if _, ok := resp["202"]; !ok {
		t.Fatalf("the host's rule governs this op, so its document must publish the held body: %v", resp)
	}
	// And the seam keeps it.
	req := httptest.NewRequest("POST", "/v1/child/act", strings.NewReader(`{"owner":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	got, err := host.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = got.Body.Close()
	if got.StatusCode != 202 {
		t.Fatalf("status = %d, want the 202 the document publishes", got.StatusCode)
	}
}

// An op that declares 202 as its OWN success keeps that meaning. The held body
// is added beside an op's responses, never over one it declared — two facts
// about one code cannot both be published, and the op's own is the older claim.
func TestDecision_ADeclared202IsNotOverwritten(t *testing.T) {
	app := zip.New(zip.Config{AppName: "queue", DisableStartupMessage: true})
	zip.Post(app, "/v1/queue", func(_ context.Context, _ *thingIn) (*thingOut, error) {
		return &thingOut{OK: true}, nil
	}, zip.WithStatus(202), zip.WithOperationID("queue"))
	app.Authorize(func(context.Context, zip.Op, any) (zip.Decision, error) {
		return zip.Decision{Effect: zip.Allow}, nil
	})
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	paths := app.OpenAPISpec()["paths"].(map[string]map[string]any)
	resp := paths["/v1/queue"]["post"].(map[string]any)["responses"].(map[string]any)
	entry, _ := resp["202"].(map[string]any)
	if entry["description"] == "held for approval" {
		t.Fatalf("the op's own 202 was overwritten by the held body: %v", entry)
	}
}

// commandFor is the CLI command that runs the op named id.
func commandFor(t *testing.T, app *zip.App, id string) zip.Command {
	t.Helper()
	for _, c := range app.Commands() {
		if c.OperationID == id {
			return c
		}
	}
	t.Fatalf("no CLI command for op %q", id)
	return zip.Command{}
}

// graphSays reports whether any graph error carries want.
func graphSays(r zip.GraphResponse, want string) bool {
	for _, e := range r.Errors {
		if strings.Contains(e.Message, want) {
			return true
		}
	}
	return false
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
