package zip_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// memberIn is a member-route input: identity in the path, payload in the body.
type memberIn struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Note  string `json:"note"`
}

type memberOut struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Note  string `json:"note"`
}

func echoMember(_ context.Context, in *memberIn) (*memberOut, error) {
	return &memberOut{Owner: in.Owner, Name: in.Name, Note: in.Note}, nil
}

func memberApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	zip.Get(a, "/v1/t/things/:owner/:name", echoMember)
	zip.Patch(a, "/v1/t/things/:owner/:name", echoMember)
	return a
}

func do(t *testing.T, a *zip.App, method, path, body string) (int, memberOut) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
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
	var out memberOut
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// A GET member route has no body at all, so the path is the ONLY source of the
// target. Before path binding this decoded to the zero value and the handler
// could not tell which resource was asked for.
func TestPathParamsBindOnGet(t *testing.T) {
	code, out := do(t, memberApp(t), "GET", "/v1/t/things/acme/widget", "")
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if out.Owner != "acme" || out.Name != "widget" {
		t.Fatalf("got (%q,%q), want (acme,widget)", out.Owner, out.Name)
	}
}

// The body still fills every field the path does not name.
func TestBodyFillsNonPathFields(t *testing.T) {
	code, out := do(t, memberApp(t), "PATCH", "/v1/t/things/acme/widget", `{"note":"hello"}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if out.Note != "hello" {
		t.Fatalf("note = %q, want hello", out.Note)
	}
	if out.Owner != "acme" || out.Name != "widget" {
		t.Fatalf("got (%q,%q), want (acme,widget)", out.Owner, out.Name)
	}
}

// THE SECURITY PROPERTY: a body that names a DIFFERENT target than the URL must
// not win. The URL routed the request and the authorizer runs on this same
// decoded value, so if the body could override the path the authorizer would
// approve one resource while the handler wrote another.
func TestPathBeatsBodyOnConflict(t *testing.T) {
	code, out := do(t, memberApp(t), "PATCH", "/v1/t/things/acme/widget",
		`{"owner":"victim","name":"secret","note":"n"}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if out.Owner != "acme" || out.Name != "widget" {
		t.Fatalf("body overrode the URL: got (%q,%q), want (acme,widget)", out.Owner, out.Name)
	}
}

// A route with no params must behave exactly as before — the body is the whole
// input. This is what guarantees the change cannot disturb existing collection
// routes.
func TestNoParamsLeavesBodyIntact(t *testing.T) {
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	zip.Post(a, "/v1/t/things", echoMember)
	code, out := do(t, a, "POST", "/v1/t/things", `{"owner":"acme","name":"widget","note":"n"}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if out.Owner != "acme" || out.Name != "widget" || out.Note != "n" {
		t.Fatalf("body binding regressed: %+v", out)
	}
}

// The authorizer must see the PATH target, since it runs on the decoded input.
// This is the property IAM's write authorization depends on.
func TestAuthorizerSeesPathTarget(t *testing.T) {
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	var sawOwner, sawName string
	a.Authorize(func(_ context.Context, _ zip.Op, in any) (zip.Decision, error) {
		if m, ok := in.(*memberIn); ok {
			sawOwner, sawName = m.Owner, m.Name
		}
		return zip.Decision{Effect: zip.Allow}, nil
	})
	zip.Patch(a, "/v1/t/things/:owner/:name", echoMember)
	if code, _ := do(t, a, "PATCH", "/v1/t/things/acme/widget", `{"owner":"victim"}`); code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if sawOwner != "acme" || sawName != "widget" {
		t.Fatalf("authorizer saw (%q,%q), want (acme,widget)", sawOwner, sawName)
	}
}

// The generated spec must declare every templated segment, or it is invalid
// OpenAPI and every generator downstream rejects it.
func TestOpenAPIDeclaresPathParams(t *testing.T) {
	a := memberApp(t)
	if err := a.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
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
	item, ok := paths["/v1/t/things/{owner}/{name}"].(map[string]any)
	if !ok {
		t.Fatalf("templated path missing from spec; got keys %v", keysOf(paths))
	}
	get, _ := item["get"].(map[string]any)
	params, _ := get["parameters"].([]any)
	// Path params first, in pattern order, required.
	if len(params) < 2 {
		t.Fatalf("parameters = %v, want the 2 path params declared", params)
	}
	for i, want := range []string{"owner", "name"} {
		p, _ := params[i].(map[string]any)
		if p["name"] != want || p["in"] != "path" || p["required"] != true {
			t.Fatalf("parameter %d = %v, want required path param %q", i, p, want)
		}
	}
	// Then the rest of the input, which a GET binds from the query string.
	// `note` is not a path segment, so ?note= is the ONLY way to supply it —
	// the document must say so or it describes a call nobody can make.
	if len(params) != 3 {
		t.Fatalf("parameters = %v, want 2 path + 1 query", params)
	}
	q, _ := params[2].(map[string]any)
	if q["name"] != "note" || q["in"] != "query" || q["required"] != false {
		t.Fatalf("parameter 2 = %v, want optional query param \"note\"", q)
	}
	// A field that IS a path segment is declared once, as a path param — never
	// duplicated into the query set.
	seen := map[string]int{}
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		seen[p["name"].(string)]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("parameter %q declared %d times, want exactly 1", name, n)
		}
	}
}

// countIn addresses a NUMBERED resource: the path segment binds to an int
// field, and `page` rides the query string of the same request.
type countIn struct {
	N    int   `json:"n"`
	Page int32 `json:"page"`
}

func echoCount(_ context.Context, in *countIn) (*countIn, error) { return in, nil }

// A path parameter's type is the type of the field it binds to. Both halves of
// the URL run through one binder (bindURL), so describing one half from the
// input type and hardcoding the other to "string" made the document contradict
// itself about the same value — and every SDK generated from it took a string
// where the handler wanted a number.
func TestOpenAPITypesPathParamsFromTheInput(t *testing.T) {
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	zip.Get(a, "/v1/t/counts/:n", echoCount)
	// :missing names no field, so nothing types it and the wire carried text.
	zip.Get(a, "/v1/t/other/:missing", echoCount)

	params := paramsOf(t, a, "/v1/t/counts/{n}")
	if len(params) != 2 {
		t.Fatalf("parameters = %v, want the path param and the query param", params)
	}
	for _, p := range params {
		decl, _ := p.(map[string]any)
		schema, _ := decl["schema"].(map[string]any)
		if schema["type"] != "integer" {
			t.Errorf("%v %q schema = %v, want integer", decl["in"], decl["name"], schema)
		}
	}
	missing, _ := paramsOf(t, a, "/v1/t/other/{missing}")[0].(map[string]any)
	if s, _ := missing["schema"].(map[string]any); s["type"] != "string" {
		t.Errorf("undeclared param schema = %v, want string", s)
	}
}

func paramsOf(t *testing.T, a *zip.App, path string) []any {
	t.Helper()
	spec := a.OpenAPISpec()
	paths, _ := spec["paths"].(map[string]map[string]any)
	item, ok := paths[path]
	if !ok {
		t.Fatalf("no path %q in spec", path)
	}
	get, _ := item["get"].(map[string]any)
	params, _ := get["parameters"].([]any)
	return params
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A path parameter is percent-encoded on the wire — it has to be: a space, a
// slash or a percent has no other spelling between two slashes. The router
// matches on the raw text, so until this was decoded a handler addressed at
// /v1/t/things/acme/café was handed "caf%C3%A9" and looked up a name nobody
// has. Every client encodes, so before this no client could make such a value
// round-trip.
func TestPathParamsArriveDecoded(t *testing.T) {
	for _, c := range []struct{ sent, want string }{
		{"plain", "plain"},
		{"a%20b", "a b"},
		{"a%2Fb", "a/b"},      // a slash inside one segment has only this spelling
		{"caf%C3%A9", "café"}, // and non-ASCII has only this one
		{"100%25", "100%"},
		{"a+b", "a+b"}, // a plus is a plus in a path; only a query reads it as a space
	} {
		code, out := do(t, memberApp(t), "GET", "/v1/t/things/acme/"+c.sent, "")
		if code != 200 {
			t.Errorf("%q: status = %d, want 200", c.sent, code)
			continue
		}
		if out.Name != c.want {
			t.Errorf("%q arrived as %q, want %q", c.sent, out.Name, c.want)
		}
	}
}

// The untyped accessor reads the same value the typed binder does. Two
// spellings of one parameter would be two answers to the same question.
func TestParamReadsWhatTheBinderBinds(t *testing.T) {
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	a.Get("/v1/t/raw/:name", func(c *zip.Ctx) error { return c.String(200, c.Param("name")) })

	req, _ := http.NewRequest("GET", "/v1/t/raw/caf%C3%A9", nil)
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "café" {
		t.Errorf("Param = %q, want %q", body, "café")
	}
}
