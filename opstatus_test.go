package zip_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The status a successful op answers with is part of its CONTRACT, so it is
// declared on the op and projected from the registry like everything else. Before
// WithStatus, zip wrote 200 (204 for a nil Out) and had no vocabulary for
// anything else, so a service that answers 201 Created had to reach around the
// framework and set it per request — a side channel that no projection could
// see. The document said 200, every generated SDK said 200, and the wire said
// 201.

type mkIn struct {
	Name string `json:"name"`
}

type mkOut struct {
	ID string `json:"id"`
}

func mk(_ context.Context, in *mkIn) (*mkOut, error) { return &mkOut{ID: in.Name}, nil }

func nilOut(context.Context, *mkIn) (*mkOut, error) { return nil, nil }

// respOf reads the one responses entry an op declares: its code and description.
func respOf(t *testing.T, a *zip.App, path, method string) (string, map[string]any) {
	t.Helper()
	paths, _ := a.OpenAPISpec()["paths"].(map[string]map[string]any)
	item, ok := paths[path]
	if !ok {
		t.Fatalf("no path %q in the document", path)
	}
	op, _ := item[method].(map[string]any)
	resp, _ := op["responses"].(map[string]any)
	if len(resp) != 1 {
		t.Fatalf("responses = %v, want exactly one declared status", resp)
	}
	for code, body := range resp {
		b, _ := body.(map[string]any)
		return code, b
	}
	return "", nil
}

// The declared status reaches the WIRE.
func TestWithStatus_ReachesTheWire(t *testing.T) {
	a := zip.New(zip.Config{AppName: "st", DisableStartupMessage: true})
	zip.Post(a, "/v1/st/things", mk, zip.WithStatus(201))

	code, body := call2(t, a, "POST", "/v1/st/things", `{"name":"x"}`)
	if code != 201 {
		t.Fatalf("status = %d, want 201", code)
	}
	if !strings.Contains(body, `"id":"x"`) {
		t.Errorf("body = %s, want the handler's result — a status must not cost the body", body)
	}
}

// …and the DOCUMENT, which is the whole point. A status that only changed the
// wire would be a prettier side channel, not a contract: an SDK generated from a
// document that says 200 treats a 201 as unexpected.
func TestWithStatus_ReachesTheDocument(t *testing.T) {
	a := zip.New(zip.Config{AppName: "st", DisableStartupMessage: true})
	zip.Post(a, "/v1/st/things", mk, zip.WithStatus(201))

	code, body := respOf(t, a, "/v1/st/things", "post")
	if code != "201" {
		t.Fatalf("responses key = %q, want %q", code, "201")
	}
	if body["description"] != "created" {
		t.Errorf("description = %v, want the reason phrase", body["description"])
	}
	// The schema still rides along: a 201 describes its body like a 200 does.
	content, _ := body["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	if _, ok := media["schema"]; !ok {
		t.Errorf("201 carries no schema; content = %v", content)
	}
}

// A void op keeps the status it declared rather than falling back to 204: the
// declaration is what the caller was promised.
func TestWithStatus_NilOutKeepsTheDeclaredStatus(t *testing.T) {
	a := zip.New(zip.Config{AppName: "st", DisableStartupMessage: true})
	zip.Post(a, "/v1/st/jobs", nilOut, zip.WithStatus(202))

	if code, body := call2(t, a, "POST", "/v1/st/jobs", `{"name":"x"}`); code != 202 || strings.TrimSpace(body) != "" {
		t.Fatalf("status = %d body = %q, want 202 with no body", code, body)
	}
	if code, resp := respOf(t, a, "/v1/st/jobs", "post"); code != "202" || resp["description"] != "accepted" {
		t.Errorf("document says %q %v, want 202 accepted", code, resp["description"])
	}
}

// Nothing changes for an op that declares no status — the defaults are the
// defaults, and a document that never asked for this does not churn.
func TestWithStatus_DefaultsAreUnchanged(t *testing.T) {
	a := zip.New(zip.Config{AppName: "st", DisableStartupMessage: true})
	zip.Post(a, "/v1/st/plain", mk)
	zip.Post(a, "/v1/st/void", nilOut)

	if code, _ := call2(t, a, "POST", "/v1/st/plain", `{"name":"x"}`); code != 200 {
		t.Errorf("status = %d, want the 200 default", code)
	}
	if code, _ := call2(t, a, "POST", "/v1/st/void", `{"name":"x"}`); code != 204 {
		t.Errorf("status = %d, want the 204 a nil Out has always meant", code)
	}
	if code, resp := respOf(t, a, "/v1/st/plain", "post"); code != "200" || resp["description"] != "ok" {
		t.Errorf("document says %q %v, want 200 ok", code, resp["description"])
	}
}

// A status is the SUCCESS status. An error status comes from the error a handler
// returns, and letting both say it would be two places for one fact — so a
// non-2xx is refused at declaration, at boot, not at request time.
func TestWithStatus_RefusesANonSuccessCode(t *testing.T) {
	for _, code := range []int{404, 500, 302, 199} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("WithStatus(%d) was accepted; a non-2xx must be refused at declaration", code)
				}
			}()
			zip.WithStatus(code)
		}()
	}
}

// The status is an HTTP notion, so the projections that are not HTTP are
// untouched: an MCP tools/call still answers with the op's result.
func TestWithStatus_MCPIsUnaffected(t *testing.T) {
	a := zip.New(zip.Config{AppName: "st", DisableStartupMessage: true})
	zip.Post(a, "/v1/st/things", mk, zip.WithStatus(201), zip.WithOperationID("st_make"))
	if err := a.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	code, body := call2(t, a, "POST", "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"st_make","arguments":{"name":"x"}}}`)
	if code != 200 {
		t.Fatalf("mcp status = %d, want 200 — JSON-RPC carries its own outcome", code)
	}
	var res struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("mcp body not json: %v — %s", err, body)
	}
	if res.Result.IsError || len(res.Result.Content) == 0 || !strings.Contains(res.Result.Content[0].Text, `"id":"x"`) {
		t.Fatalf("mcp result = %+v, want the op's own output", res.Result)
	}
}
