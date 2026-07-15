package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

type thingIn struct {
	Owner string `json:"owner"`
}
type thingOut struct {
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
	app.Authorize(func(_ context.Context, op zip.Op, in any) error {
		ti, ok := in.(*thingIn)
		if !ok {
			t.Fatalf("authorizer got %T, want *thingIn (the decoded input)", in)
		}
		seen = append(seen, op.OperationID+"/"+op.Method+":"+ti.Owner)
		if ti.Owner != "self" {
			return zip.ErrForbidden("forbidden")
		}
		return nil
	})
	app.Prepare() // install the deferred /mcp route so tools/call is reachable

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
