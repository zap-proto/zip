package zip

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type reportIn struct{}
type reportOut struct {
	Body string `json:"body"`
}

// The ANSWER states its headers, the way it now states its code.
func (r *reportOut) ResponseHeaders() map[string]string {
	return map[string]string{"Cache-Control": "no-store", "Set-Cookie": "sid=abc; HttpOnly"}
}

// Gap 3: a typed op sets response headers and a cookie with no middleware, and
// both land in the document because both are part of the contract.
func TestResponseHeader_SetAndPublished(t *testing.T) {
	app := quiet("svc")
	Get(app, "/v1/report", func(_ context.Context, _ *reportIn) (*reportOut, error) {
		return &reportOut{Body: "ok"}, nil
	}, WithOperationID("report"), WithResponseHeader("Cache-Control", "Set-Cookie"))

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/report", nil))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("Set-Cookie"); !strings.Contains(got, "sid=abc") {
		t.Errorf("Set-Cookie = %q — a session cookie from a typed op, with no middleware", got)
	}

	// Published: a header a client relies on is in the document.
	spec := app.OpenAPISpec()
	get := spec["paths"].(map[string]map[string]any)["/v1/report"]["get"].(map[string]any)
	raw, _ := json.Marshal(get["responses"])
	for _, want := range []string{"Cache-Control", "Set-Cookie"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("document does not declare response header %q: %s", want, raw)
		}
	}
}

type sneakHdrOut struct{}

func (sneakHdrOut) ResponseHeaders() map[string]string {
	return map[string]string{"X-Undeclared": "1"}
}

// Undeclared is refused, not written — same rule an undeclared status obeys.
func TestResponseHeader_UndeclaredIsRefused(t *testing.T) {
	app := quiet("svc")
	Get(app, "/v1/sneak", func(_ context.Context, _ *reportIn) (*sneakHdrOut, error) {
		return &sneakHdrOut{}, nil
	}, WithOperationID("sneakhdr"))

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/sneak", nil))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("X-Undeclared"); got != "" {
		t.Errorf("an undeclared response header reached the wire: %q", got)
	}
	if resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if body := readAll(t, resp.Body); !strings.Contains(body, "WithResponseHeader") {
		t.Errorf("refusal does not name the fix: %s", body)
	}
}

// Over MCP there is no per-op response — one HTTP response carries the whole
// JSON-RPC envelope — so declared headers are NOT written. The answer's VALUE is
// identical, and the declared set still appears in the schema. Defined, tested,
// not silent.
func TestResponseHeader_NotWrittenOverMCP(t *testing.T) {
	app := quiet("svc")
	Get(app, "/v1/report", func(_ context.Context, _ *reportIn) (*reportOut, error) {
		return &reportOut{Body: "ok"}, nil
	}, WithOperationID("report"), WithResponseHeader("Cache-Control", "Set-Cookie"))
	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	req := postReq("/mcp", "application/json",
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"report","arguments":{}}}`))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("a per-op Set-Cookie rode the JSON-RPC envelope: %q", got)
	}
	// The value is unchanged on every transport.
	if body := readAll(t, resp.Body); !strings.Contains(body, `\"body\":\"ok\"`) {
		t.Errorf("the answer differs over MCP: %s", body)
	}
}

// header:"Host" already binds — fasthttp surfaces Host through the same
// accessor — so a brand-resolving op needs no new spelling.
type brandIn struct {
	Host string `json:"host" header:"Host"`
}
type brandOut struct {
	Body string `json:"body"`
}

func TestResponseHeader_HostBindsWithNoNewSpelling(t *testing.T) {
	app := quiet("svc")
	Get(app, "/v1/brand", func(_ context.Context, in *brandIn) (*brandOut, error) {
		return &brandOut{Body: in.Host}, nil
	}, WithOperationID("brand"))

	resp, err := app.Test(httptest.NewRequest("GET", "http://brand.example.com/v1/brand", nil))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body := readAll(t, resp.Body); body != `{"body":"brand.example.com"}` {
		t.Errorf(`header:"Host" did not bind: %s`, body)
	}
}
