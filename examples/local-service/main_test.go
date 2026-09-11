package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// newService is the service over a fresh SQLite file.
func newService(t *testing.T) (*zip.App, *Store) {
	t.Helper()
	s := &Store{}
	if err := s.Open(filepath.Join(t.TempDir(), "notes.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	return New(s), s
}

// call sends one HTTP request to the app without opening a socket.
func call(t *testing.T, app *zip.App, method, path, body string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

// TestEveryOperationIsInEveryProjection reads the router and checks that each
// typed operation is also an OpenAPI operation, an MCP tool and a CLI command,
// under one id. An operation added to New is covered with no change here.
func TestEveryOperationIsInEveryProjection(t *testing.T) {
	app, _ := newService(t)

	_, _, body := call(t, app, "GET", zip.SpecPath, "")
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Description string `json:"description"`
		} `json:"paths"`
	}
	if err := json.Unmarshal([]byte(body), &spec); err != nil {
		t.Fatalf("OpenAPI document: %v", err)
	}

	_, _, body = call(t, app, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	tools := map[string]bool{}
	for _, tool := range list.Result.Tools {
		tools[tool.Name] = true
	}

	commands := map[string]bool{}
	for _, c := range app.Commands() {
		commands[c.OperationID] = true
	}

	ops := 0
	for _, r := range app.Routes() {
		if r.Op == "" {
			continue // an untyped route has no projections
		}
		ops++
		op := spec.Paths[zip.Template(r.Pattern)][strings.ToLower(r.Method)]
		if op.OperationID != r.Op {
			t.Errorf("%s %s: OpenAPI operationId %q, want %q", r.Method, r.Pattern, op.OperationID, r.Op)
		}
		if op.Description == "" {
			t.Errorf("%s %s: no description; run go generate", r.Method, r.Pattern)
		}
		if !tools[r.Op] {
			t.Errorf("%s %s: no MCP tool %q", r.Method, r.Pattern, r.Op)
		}
		if !commands[r.Op] {
			t.Errorf("%s %s: no CLI command %q", r.Method, r.Pattern, r.Op)
		}
	}
	if ops == 0 {
		t.Fatal("no typed operations registered")
	}
}

// TestOneHandlerBehindEveryDoor calls the operation over REST, MCP and the CLI,
// then counts the rows: three doors, one handler, one table.
func TestOneHandlerBehindEveryDoor(t *testing.T) {
	app, s := newService(t)

	code, _, body := call(t, app, "POST", "/v1/notes", `{"text":"over REST"}`)
	if code != 200 || body != `{"id":1,"text":"over REST"}` {
		t.Errorf("REST: %d %s", code, body)
	}

	_, _, body = call(t, app, "POST", "/mcp",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"post_notes","arguments":{"text":"over MCP"}}}`)
	if !strings.Contains(body, `{\"id\":2,\"text\":\"over MCP\"}`) {
		t.Errorf("MCP: %s", body)
	}

	var out bytes.Buffer
	cli := app.CLI()
	cli.Out = &out
	if err := cli.Run(context.Background(), []string{"notes", "create", "--text", "over the CLI"}); err != nil {
		t.Fatalf("CLI: %v", err)
	}
	if !strings.Contains(out.String(), `"id": 3`) {
		t.Errorf("CLI: %s", out.String())
	}

	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM notes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("notes table has %d rows, want 3", n)
	}
}

// TestARefusalIsAProblemDocument checks the error body: RFC 9457, not a shape of
// zip's own.
func TestARefusalIsAProblemDocument(t *testing.T) {
	app, _ := newService(t)

	code, typ, body := call(t, app, "POST", "/v1/notes", `{}`)
	if code != 400 || typ != "application/problem+json" {
		t.Fatalf("empty note: %d %s %s", code, typ, body)
	}
	var problem struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Type != "about:blank" || problem.Title != "Bad Request" || problem.Status != 400 || problem.Detail == "" {
		t.Errorf("problem document: %s", body)
	}
}
