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
	a.Authorize(func(_ context.Context, _ zip.Op, in any) error {
		if m, ok := in.(*memberIn); ok {
			sawOwner, sawName = m.Owner, m.Name
		}
		return nil
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
	item, ok := paths["/v1/t/things/{owner}/{name}"].(map[string]any)
	if !ok {
		t.Fatalf("templated path missing from spec; got keys %v", keysOf(paths))
	}
	get, _ := item["get"].(map[string]any)
	params, _ := get["parameters"].([]any)
	if len(params) != 2 {
		t.Fatalf("parameters = %v, want 2 declared", params)
	}
	for i, want := range []string{"owner", "name"} {
		p, _ := params[i].(map[string]any)
		if p["name"] != want || p["in"] != "path" || p["required"] != true {
			t.Fatalf("parameter %d = %v, want required path param %q", i, p, want)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
