package zip_test

// The query string is half the URL, and until a typed op could read it, every
// route carrying `?q=` had to stay an untyped handler — invisible to OpenAPI and
// absent from MCP. These pin the binder's contract: what binds, from where, and
// who wins when two sources name the same field.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

func stringReader(s string) io.Reader { return strings.NewReader(s) }

// searchIn exercises every kind the URL binder converts, plus a body-only
// field, plus a path param that shares its name with a query key.
type searchIn struct {
	Owner   string   `json:"owner"` // path param on the member route
	Q       string   `json:"q"`
	Limit   int      `json:"limit"`
	Offset  uint32   `json:"offset"`
	Score   float64  `json:"score"`
	Debug   bool     `json:"debug"`
	Ignored []string `json:"ignored"` // not a scalar: never URL-bound
}

type searchOut struct {
	Owner   string   `json:"owner"`
	Q       string   `json:"q"`
	Limit   int      `json:"limit"`
	Offset  uint32   `json:"offset"`
	Score   float64  `json:"score"`
	Debug   bool     `json:"debug"`
	Ignored []string `json:"ignored"`
}

func echoSearch(_ context.Context, in *searchIn) (*searchOut, error) {
	return &searchOut{
		Owner: in.Owner, Q: in.Q, Limit: in.Limit, Offset: in.Offset,
		Score: in.Score, Debug: in.Debug, Ignored: in.Ignored,
	}, nil
}

func searchApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	zip.Get(a, "/v1/t/search", echoSearch)
	zip.Get(a, "/v1/t/orgs/:owner/search", echoSearch)
	zip.Post(a, "/v1/t/orgs/:owner/search", echoSearch)
	return a
}

func getSearch(t *testing.T, a *zip.App, method, path, body string) (int, searchOut) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = stringReader(body)
	}
	req, err := http.NewRequest(method, path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out searchOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s %s: response is not %T: %s", method, path, out, raw)
	}
	return resp.StatusCode, out
}

// A GET typed op reads its whole input from the URL.
func TestTypedGetBindsQueryString(t *testing.T) {
	a := searchApp(t)
	status, out := getSearch(t, a, "GET",
		"/v1/t/search?q=hello+world&limit=25&offset=100&score=1.5&debug=true", "")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if out.Q != "hello world" {
		t.Errorf("q = %q, want %q", out.Q, "hello world")
	}
	if out.Limit != 25 {
		t.Errorf("limit = %d, want 25", out.Limit)
	}
	if out.Offset != 100 {
		t.Errorf("offset = %d, want 100", out.Offset)
	}
	if out.Score != 1.5 {
		t.Errorf("score = %v, want 1.5", out.Score)
	}
	if !out.Debug {
		t.Errorf("debug = false, want true")
	}
}

// A bare flag is present-means-true, the convention forms and CLIs already use.
func TestTypedGetBindsBareBoolFlag(t *testing.T) {
	a := searchApp(t)
	if _, out := getSearch(t, a, "GET", "/v1/t/search?debug", ""); !out.Debug {
		t.Errorf("?debug did not set the flag")
	}
	if _, out := getSearch(t, a, "GET", "/v1/t/search?debug=false", ""); out.Debug {
		t.Errorf("?debug=false set the flag")
	}
}

// One bad scalar does not fail the call. `?limit=abc` is a typo about ONE field;
// refusing the request would make a typed GET brittler than the untyped handler
// it replaced.
func TestTypedGetIgnoresUnparseableScalar(t *testing.T) {
	a := searchApp(t)
	status, out := getSearch(t, a, "GET", "/v1/t/search?q=ok&limit=abc", "")
	if status != 200 {
		t.Fatalf("status = %d, want 200 (a bad scalar is not a bad request)", status)
	}
	if out.Q != "ok" {
		t.Errorf("q = %q, want the good field still bound", out.Q)
	}
	if out.Limit != 0 {
		t.Errorf("limit = %d, want the zero value", out.Limit)
	}
}

// Unknown keys are ordinary — callers append tracking params — so they are
// ignored, never rejected.
func TestTypedGetIgnoresUnknownQueryKeys(t *testing.T) {
	a := searchApp(t)
	status, out := getSearch(t, a, "GET", "/v1/t/search?q=ok&utm_source=x&nope=1", "")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if out.Q != "ok" {
		t.Errorf("q = %q, want %q", out.Q, "ok")
	}
}

// Non-scalar fields are not URL-bindable and are left alone.
func TestTypedGetLeavesNonScalarsUnbound(t *testing.T) {
	a := searchApp(t)
	if _, out := getSearch(t, a, "GET", "/v1/t/search?ignored=a,b", ""); out.Ignored != nil {
		t.Errorf("ignored = %v, want nil (a slice is not URL-bindable)", out.Ignored)
	}
}

// The URL is the addressing authority: the path segment the router MATCHED on
// beats a query key of the same name.
func TestPathParamBeatsQueryParam(t *testing.T) {
	a := searchApp(t)
	_, out := getSearch(t, a, "GET", "/v1/t/orgs/acme/search?owner=evil&q=x", "")
	if out.Owner != "acme" {
		t.Errorf("owner = %q, want %q — the routed path must win", out.Owner, "acme")
	}
}

// ...and the query beats the body, because it is part of the URL; the path
// still beats both.
func TestQueryBeatsBodyAndPathBeatsQuery(t *testing.T) {
	a := searchApp(t)
	_, out := getSearch(t, a, "POST", "/v1/t/orgs/acme/search?q=fromquery",
		`{"owner":"evil","q":"frombody","limit":7}`)
	if out.Owner != "acme" {
		t.Errorf("owner = %q, want %q — path wins", out.Owner, "acme")
	}
	if out.Q != "fromquery" {
		t.Errorf("q = %q, want %q — query beats body", out.Q, "fromquery")
	}
	if out.Limit != 7 {
		t.Errorf("limit = %d, want 7 — a body-only field still binds", out.Limit)
	}
}

// The document must name every query parameter the binder reads, or it
// describes a route nobody can call correctly.
func TestOpenAPIDeclaresQueryParams(t *testing.T) {
	a := searchApp(t)
	a.Prepare()
	req, _ := http.NewRequest("GET", "/.well-known/openapi.json", nil)
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("spec is not json: %v", err)
	}
	paths, _ := spec["paths"].(map[string]any)
	item, _ := paths["/v1/t/search"].(map[string]any)
	get, _ := item["get"].(map[string]any)
	params, _ := get["parameters"].([]any)

	got := map[string]map[string]any{}
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		got[p["name"].(string)] = p
	}
	for name, wantType := range map[string]string{
		"q": "string", "limit": "integer", "offset": "integer",
		"score": "number", "debug": "boolean", "owner": "string",
	} {
		p, ok := got[name]
		if !ok {
			t.Errorf("query parameter %q missing from the document", name)
			continue
		}
		if p["in"] != "query" {
			t.Errorf("parameter %q in = %v, want query", name, p["in"])
		}
		schema, _ := p["schema"].(map[string]any)
		if schema["type"] != wantType {
			t.Errorf("parameter %q type = %v, want %s", name, schema["type"], wantType)
		}
	}
	// A non-scalar is not declared, because the binder cannot fill it.
	if _, ok := got["ignored"]; ok {
		t.Errorf("declared a query parameter the binder cannot bind")
	}
	// A POST declares a requestBody instead of query parameters — the two are
	// the same rule (hasBody) read from opposite sides.
	postItem, _ := paths["/v1/t/orgs/{owner}/search"].(map[string]any)
	post, _ := postItem["post"].(map[string]any)
	if _, ok := post["requestBody"]; !ok {
		t.Errorf("POST has no requestBody")
	}
	for _, raw := range post["parameters"].([]any) {
		p, _ := raw.(map[string]any)
		if p["in"] != "path" {
			t.Errorf("POST declares %v; a body method takes no query params", p)
		}
	}
}

// MCP is unaffected: a tools/call carries every argument in its JSON object, so
// the tool's input is the whole struct with no URL involved.
func TestMCPCallIsUnaffectedByQueryBinding(t *testing.T) {
	a := searchApp(t)
	a.Prepare()
	req, _ := http.NewRequest("POST", "/mcp", stringReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_v1_t_search","arguments":{"q":"viajson","limit":9}}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("mcp response is not json: %s", raw)
	}
	if env.Result.IsError || len(env.Result.Content) == 0 {
		t.Fatalf("tools/call failed: %s", raw)
	}
	var out searchOut
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &out); err != nil {
		t.Fatalf("tool result is not %T: %s", out, env.Result.Content[0].Text)
	}
	if out.Q != "viajson" || out.Limit != 9 {
		t.Errorf("tool result = %+v, want the JSON arguments bound", out)
	}
}
