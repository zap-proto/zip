package zip_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// scriptIn is PUT /v1/workers/scripts/:script — the route where a path
// parameter's NAME and a body field's name are the same word for two different
// things: `script` in the URL is which worker, `script` in the body is its
// source code.
//
// Typed with one name for both, bindURL bound the path last (it is the
// addressing authority, which is right) and deployed a worker whose code was its
// own name. So the route could not be typed at all — a silent corruption if
// anyone typed it naively.
//
// `url:` names the field the URL carries, the way `json:` names the one the body
// carries; "-" opts out of the other half entirely.
type scriptIn struct {
	// Name is which worker to deploy — the URL says so, and only the URL.
	Name string `json:"-" url:"script"`
	// Script is the worker's source. The body says so, and only the body.
	Script string `json:"script" url:"-"`
	// Rollout rides either half, like every other field.
	Rollout int `json:"rollout"`
}

type scriptOut struct {
	Name    string `json:"name"`
	Script  string `json:"script"`
	Rollout int    `json:"rollout"`
}

func putScript(_ context.Context, in *scriptIn) (*scriptOut, error) {
	return &scriptOut{Name: in.Name, Script: in.Script, Rollout: in.Rollout}, nil
}

func scriptApp() *zip.App {
	a := zip.New(zip.Config{AppName: "workers", DisableStartupMessage: true})
	zip.Put(a, "/v1/workers/scripts/:script", putScript)
	return a
}

// The body field keeps its value and the URL field gets the URL's.
func TestURLTag_PathParamAndBodyFieldMayShareAName(t *testing.T) {
	code, body := call2(t, scriptApp(), "PUT", "/v1/workers/scripts/hello",
		`{"script":"export default {}","rollout":10}`)
	if code != 200 {
		t.Fatalf("PUT = %d %s, want 200", code, body)
	}
	for _, want := range []string{`"name":"hello"`, `"script":"export default {}"`, `"rollout":10`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s is missing %s", body, want)
		}
	}
}

// And the document describes exactly that: `script` is a path parameter typed
// from the field that receives it, and the request body carries the source.
func TestURLTag_DocumentDescribesBothHalves(t *testing.T) {
	a := scriptApp()
	params := paramsOf2(t, a, "/v1/workers/scripts/{script}", "put")
	if len(params) != 1 {
		t.Fatalf("parameters = %v, want just the path param", params)
	}
	p, _ := params[0].(map[string]any)
	if p["name"] != "script" || p["in"] != "path" {
		t.Errorf("parameter = %v, want script in path", p)
	}

	spec := a.OpenAPISpec()
	paths, _ := spec["paths"].(map[string]map[string]any)
	op, _ := paths["/v1/workers/scripts/{script}"]["put"].(map[string]any)
	rb, _ := op["requestBody"].(map[string]any)
	if rb == nil {
		t.Fatal("no requestBody: the source code has nowhere to ride")
	}
	content, _ := rb["content"].(map[string]any)["application/json"].(map[string]any)
	schema, _ := content["schema"].(map[string]any)
	if ref, ok := schema["$ref"].(string); ok {
		defs, _ := spec["components"].(map[string]any)["schemas"].(map[string]any)
		schema, _ = defs[ref[len("#/components/schemas/"):]].(map[string]any)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["script"]; !ok {
		t.Errorf("request body has no `script`: %v", props)
	}
	if len(props) != 2 {
		t.Errorf("request body properties = %v, want script and rollout", props)
	}
}

// The command carries both halves too, and this is where the collision bit
// hardest: the CLI drops a flag whose name a path param already claims, so the
// body field shared a name with the path and the command could not send the
// source AT ALL. What decides is which half of the request carries the field, not
// which word it is spelled with.
func TestURLTag_CommandCarriesBothHalves(t *testing.T) {
	a := scriptApp()
	cmds := a.Commands()
	if len(cmds) != 1 {
		t.Fatalf("commands = %d, want 1", len(cmds))
	}
	c := cmds[0]
	if len(c.Args) != 1 || c.Args[0].Name != "script" {
		t.Errorf("args = %+v, want the path's script", c.Args)
	}
	var flags []string
	for _, f := range c.Flags {
		flags = append(flags, f.Name)
	}
	if len(flags) != 2 || flags[0] != "script" || flags[1] != "rollout" {
		t.Fatalf("flags = %v, want --script (the source) and --rollout", flags)
	}

	// And it round-trips through the invoker: the positional fills the URL, the
	// flag fills the body, and the handler sees two different values.
	out, err := zip.LocalInvoke(context.Background(), c,
		map[string]string{"script": "hello"}, []byte(`{"script":"export default {}"}`))
	if err != nil {
		t.Fatalf("LocalInvoke: %v", err)
	}
	got, _ := out.(*scriptOut)
	if got == nil || got.Name != "hello" || got.Script != "export default {}" {
		t.Errorf("out = %+v, want name hello and the source", out)
	}
}

// A `url:"-"` field is not a query parameter either — one tag, one meaning, both
// halves of the URL. Offering `?script=` would promise a binding that no longer
// happens.
func TestURLTag_OptOutIsNotAQueryParameter(t *testing.T) {
	type listIn struct {
		Query  string `json:"q"`
		Secret string `json:"secret" url:"-"`
	}
	a := zip.New(zip.Config{AppName: "workers2", DisableStartupMessage: true})
	zip.Get(a, "/v1/workers/scripts", func(_ context.Context, in *listIn) (*scriptOut, error) {
		return &scriptOut{Name: in.Query + in.Secret}, nil
	})

	for _, raw := range paramsOf2(t, a, "/v1/workers/scripts", "get") {
		if p, _ := raw.(map[string]any); p["name"] == "secret" {
			t.Errorf("secret is declared as a %v parameter", p["in"])
		}
	}
	if code, body := call2(t, a, "GET", "/v1/workers/scripts?q=a&secret=b", ""); code != 200 || !strings.Contains(body, `"name":"a"`) {
		t.Errorf("GET = %d %s, want the query to fill q and NOT secret", code, body)
	}
}
