package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A bodyless POST is an op that acts on what its URL already names: start the
// caller org's KYC, enable a flow, sweep a queue. There is nothing for the
// caller to send, and the handler never reads a body.
//
// Body-ness used to be a property of the METHOD alone, so a POST published
// `requestBody: {required: true}` whatever it read — and the document is not a
// comment, it is what the SDK, the CLI and the "try it" button are built from.
// Six company ops shipped that way: a mandatory argument over an empty object,
// for a body nothing has ever looked at. WithoutBody moves the answer to the op.

// kycIn is the input those ops actually have: NAMED, so it earns a schema in
// components — which is exactly how the lie became visible in the document.
type kycIn struct{}

type kycOut struct {
	Status string `json:"status"`
}

func startKYC(_ context.Context, _ *kycIn) (*kycOut, error) {
	return &kycOut{Status: "pending"}, nil
}

// launchIn is the other bodyless shape: the URL carries the whole input. One
// field of each kind that matters — a path segment, a query-carriable scalar,
// and a slice no URL can carry.
type launchIn struct {
	ID    string   `json:"id"`
	Force bool     `json:"force"`
	Tags  []string `json:"tags"`
}

func launch(_ context.Context, in *launchIn) (*launchIn, error) { return in, nil }

func kycApp(t *testing.T, opts ...zip.OpOption) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "company", DisableStartupMessage: true})
	zip.Post(a, "/v1/company/kyc", startKYC, opts...)
	return a
}

// The headline: a POST that declares it takes nothing publishes no requestBody
// at all — and the empty schema it used to demand is gone from components with
// it, because nothing refers to it any more.
func TestWithoutBody_PublishesNoRequestBody(t *testing.T) {
	// The failure this replaces, pinned so it cannot come back silently.
	lying := wbOp(t, kycApp(t), "/v1/company/kyc", "post")
	rb, ok := lying["requestBody"].(map[string]any)
	if !ok {
		t.Fatalf("a plain POST should still publish a requestBody; got %v", wbKeys(lying))
	}
	if rb["required"] != true {
		t.Errorf("the requestBody it used to publish was required:true; got %v", rb["required"])
	}
	if got := wbBodyRef(t, rb); got != "#/components/schemas/kycIn" {
		t.Errorf("requestBody schema = %q, want the empty input's own $ref", got)
	}
	if defs := wbSchemas(t, kycApp(t)); defs["kycIn"] == nil {
		t.Errorf("the empty input should be in components while something refers to it: %v", wbKeys(defs))
	}

	// And with the declaration.
	honest := wbOp(t, kycApp(t, zip.WithoutBody()), "/v1/company/kyc", "post")
	if _, ok := honest["requestBody"]; ok {
		t.Fatalf("WithoutBody still published a requestBody: %v", honest["requestBody"])
	}
	if _, ok := honest["parameters"]; ok {
		t.Errorf("an op with nothing to send declares no parameters either: %v", honest["parameters"])
	}
	if defs := wbSchemas(t, kycApp(t, zip.WithoutBody())); defs["kycIn"] != nil {
		t.Errorf("nothing refers to kycIn now, so it should not be published: %v", wbKeys(defs))
	}

	// The RESPONSE half is untouched: what an op sends back has nothing to do
	// with whether it reads a body, and a document that dropped its 200 would
	// have traded one lie for another.
	resp, _ := honest["responses"].(map[string]any)
	got, ok := resp["200"].(map[string]any)
	if !ok {
		t.Fatalf("responses = %v, want the 200 the op still answers with", resp)
	}
	if _, ok := got["content"]; !ok {
		t.Errorf("the 200 lost its content schema: %v", got)
	}
}

// The route stops reading a body it does not declare. Before, an op with
// nothing to send still ran its input through the decoder, so a caller who sent
// anything unparseable — or a proxy that appended a byte — got a 400 from a
// route that would not have looked at the body either way.
func TestWithoutBody_RouteIgnoresTheBodyItNeverDeclared(t *testing.T) {
	// The failure it replaces.
	code, body := call2(t, kycApp(t), "POST", "/v1/company/kyc", "not json")
	if code != 400 || !strings.Contains(body, "invalid body") {
		t.Fatalf("a plain POST with a bad body = %d %s, want 400 invalid body", code, body)
	}

	code, body = call2(t, kycApp(t, zip.WithoutBody()), "POST", "/v1/company/kyc", "not json")
	if code != 200 || !strings.Contains(body, `"status":"pending"`) {
		t.Fatalf("WithoutBody with a bad body = %d %s, want the handler to have run", code, body)
	}

	// A well-formed body is not an error either — the op simply has nowhere to
	// put it, exactly as a DELETE has never had anywhere to put one. What the
	// URL carries is what the handler sees.
	a := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Post(a, "/v1/flows/launch", launch, zip.WithoutBody())

	code, body = call2(t, a, "POST", "/v1/flows/launch", `{"force":true,"id":"from-the-body"}`)
	if code != 200 {
		t.Fatalf("POST with a smuggled body = %d %s, want 200", code, body)
	}
	if !strings.Contains(body, `"force":false`) || !strings.Contains(body, `"id":""`) {
		t.Errorf("body %s was read after all — a bodyless op must not bind from it", body)
	}
	code, body = call2(t, a, "POST", "/v1/flows/launch?force=true&id=from-the-url", "")
	if code != 200 || !strings.Contains(body, `"force":true`) || !strings.Contains(body, `"id":"from-the-url"`) {
		t.Errorf("POST with a query = %d %s, want both values bound from the URL", code, body)
	}
}

// With no requestBody to hold them, the URL-borne fields are declared where the
// binder actually reads them: as query parameters. A document that published
// neither described a call nobody could make correctly.
func TestWithoutBody_UrlFieldsAreDeclaredAsQueryParameters(t *testing.T) {
	a := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Post(a, "/v1/flows/:id/launch", launch, zip.WithoutBody())

	op := wbOp(t, a, "/v1/flows/{id}/launch", "post")
	if _, ok := op["requestBody"]; ok {
		t.Fatalf("WithoutBody still published a requestBody: %v", op["requestBody"])
	}
	got := map[string]string{}
	for _, raw := range op["parameters"].([]any) {
		p := raw.(map[string]any)
		schema, _ := p["schema"].(map[string]any)
		got[p["name"].(string)] = fmt.Sprintf("%v:%v", p["in"], schema["type"])
	}
	want := map[string]string{"id": "path:string", "force": "query:boolean"}
	if !reflect.DeepEqual(got, want) {
		// `tags` is deliberately absent: a slice cannot ride a URL, so naming it
		// would promise a parameter the binder silently drops.
		t.Fatalf("parameters = %v, want %v", got, want)
	}

	// And the wire agrees with the document it just published.
	code, body := call2(t, a, "POST", "/v1/flows/t_1/launch?force=true", "")
	if code != 200 || !strings.Contains(body, `"id":"t_1"`) || !strings.Contains(body, `"force":true`) {
		t.Fatalf("POST %s = %d %s, want both parameters bound", "/v1/flows/t_1/launch?force=true", code, body)
	}
}

// The CLI is derived from the same predicate, so a bodyless POST offers exactly
// the flags the URL can carry. Offering the rest is what made `--tags '["a"]'`
// marshal fine, go out as a query value and be dropped by the binder: a flag the
// command advertised and the wire ignored.
func TestWithoutBody_CLIOffersOnlyWhatTheUrlCanCarry(t *testing.T) {
	a := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Post(a, "/v1/flows/:id/launch", launch, zip.WithoutBody())
	zip.Post(a, "/v1/flows/:id/deploy", launch) // the body-carrying twin, unchanged

	cmds := map[string]zip.Command{}
	for _, c := range a.Commands() {
		cmds[c.Name] = c
	}
	bodyless, plain := cmds["launch"], cmds["deploy"]
	if !bodyless.NoBody {
		t.Errorf("the bodyless command does not carry the declaration: %+v", bodyless)
	}
	if plain.NoBody {
		t.Errorf("a plain POST command must be untouched: %+v", plain)
	}
	if got := wbFlags(bodyless); !reflect.DeepEqual(got, []string{"force"}) {
		t.Errorf("bodyless flags = %v, want just --force — a slice cannot ride a URL", got)
	}
	if got := wbFlags(plain); !reflect.DeepEqual(got, []string{"force", "tags"}) {
		t.Errorf("body-carrying flags = %v, want both — a body carries anything", got)
	}
	if got := wbArgs(bodyless); !reflect.DeepEqual(got, []string{"id"}) {
		t.Errorf("bodyless args = %v, want the path parameter", got)
	}
}

// The declaration has to survive the document, because that is the only thing a
// client which links none of the service's code ever reads. A command derived
// from the spec and a command derived from the registry are the same command —
// including how it sends its request.
func TestWithoutBody_SpecAndRegistryAgree(t *testing.T) {
	a := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Post(a, "/v1/flows/:id/launch", launch, zip.WithoutBody())
	zip.Post(a, "/v1/flows/:id/deploy", launch)
	zip.Get(a, "/v1/flows/:id", launch)
	zip.Delete(a, "/v1/flows/:id", launch)

	spec, err := json.Marshal(a.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	fromSpec, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	fromRegistry := a.Commands()
	if len(fromSpec) != len(fromRegistry) {
		t.Fatalf("spec has %d commands, registry has %d", len(fromSpec), len(fromRegistry))
	}
	for i := range fromRegistry {
		r, s := fromRegistry[i], fromSpec[i]
		if r.Name != s.Name || r.Method != s.Method {
			t.Fatalf("op %d: spec %s %s, registry %s %s", i, s.Method, s.Name, r.Method, r.Name)
		}
		// NoBody is the override, so it is EQUAL and not merely compatible: a
		// method that never carried a body needs none.
		if r.NoBody != s.NoBody {
			t.Errorf("%s %s NoBody: spec %v, registry %v", r.Method, r.Name, s.NoBody, r.NoBody)
		}
		if fmt.Sprint(r.Flags) != fmt.Sprint(s.Flags) {
			t.Errorf("%s %s flags: spec %v, registry %v", r.Method, r.Name, s.Flags, r.Flags)
		}
		if fmt.Sprint(r.Args) != fmt.Sprint(s.Args) {
			t.Errorf("%s %s args: spec %v, registry %v", r.Method, r.Name, s.Args, r.Args)
		}
	}
	// The one that matters: the bodyless POST is the only op whose method would
	// have carried a body and does not.
	for _, c := range fromSpec {
		if want := c.Name == "launch"; c.NoBody != want {
			t.Errorf("%s %s NoBody = %v, want %v", c.Method, c.Name, c.NoBody, want)
		}
	}
}

// End to end, the case the whole gap is about: a client that links none of the
// service asks it what it can do, and sends the request the answer describes.
// The route no longer reads a body, so a client still sending one would lose the
// flag — seeing it arrive is proof the query string was used.
func TestWithoutBody_RemoteSendsTheQueryNotABody(t *testing.T) {
	a := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Post(a, "/v1/flows/:id/launch", launch, zip.WithoutBody())
	zip.Post(a, "/v1/company/kyc", startKYC, zip.WithoutBody())

	addr := freeAddr(t)
	go func() { _ = a.Listen("http://" + addr) }()
	defer func() { _ = a.Shutdown() }()
	waitHTTP(t, "http://"+addr+"/.well-known/openapi.json")

	remote := zip.Remote{Base: "http://" + addr}
	spec, err := remote.Spec(context.Background())
	if err != nil {
		t.Fatalf("fetch spec: %v", err)
	}
	cmds, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	var out bytes.Buffer
	cli := &zip.CLI{Name: "hanzo", Commands: cmds, Invoke: remote.Invoke, Out: &out}

	if err := cli.Run(context.Background(), []string{"flows", "launch", "t_1", "--force"}); err != nil {
		t.Fatalf("remote run: %v", err)
	}
	var got launchIn
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if got.ID != "t_1" || !got.Force {
		t.Fatalf("service saw %+v — the flag rode a body the route no longer reads", got)
	}

	// An op with nothing to send is a command with nothing to give it.
	out.Reset()
	if err := cli.Run(context.Background(), []string{"company", "kyc-create"}); err != nil {
		t.Fatalf("remote run of a bodyless op with no input: %v", err)
	}
	var kyc kycOut
	if err := json.Unmarshal(out.Bytes(), &kyc); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if kyc.Status != "pending" {
		t.Fatalf("output %q, want the handler's answer", out.String())
	}
}

// WithoutBody is a statement about the HTTP wire and only about it. Addressing
// an op by NAME has no URL to carry half the input in, so over MCP the arguments
// object is still the whole input — the tool schema must not lose the fields a
// tools/call is the only way to send.
func TestWithoutBody_ByNameStillTakesTheWholeInput(t *testing.T) {
	plain := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Post(plain, "/v1/flows/:id/launch", launch, zip.WithOperationID("launch"))

	bodyless := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Post(bodyless, "/v1/flows/:id/launch", launch, zip.WithOperationID("launch"), zip.WithoutBody())

	if !reflect.DeepEqual(plain.MCPTools(), bodyless.MCPTools()) {
		t.Fatalf("WithoutBody changed the tool surface:\n plain %v\n bodyless %v", plain.MCPTools(), bodyless.MCPTools())
	}
	props, _ := bodyless.MCPTools()[0]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, want := range []string{"id", "force", "tags"} {
		if props[want] == nil {
			t.Errorf("inputSchema lost %q: %v", want, props)
		}
	}

	// And a tools/call still binds every one of them, over the same handler.
	addr := freeAddr(t)
	go func() { _ = bodyless.Listen("http://" + addr) }()
	defer func() { _ = bodyless.Shutdown() }()
	waitHTTP(t, "http://"+addr+"/.well-known/openapi.json")

	res := rpc(t, "http://"+addr+"/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"launch","arguments":{"id":"t_1","force":true,"tags":["a"]}}}`)
	result, _ := res["result"].(map[string]any)
	if result == nil || result["isError"] == true {
		t.Fatalf("tools/call on a bodyless op failed: %v", res)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{`"id":"t_1"`, `"force":true`, `"tags":["a"]`} {
		if !strings.Contains(text, want) {
			t.Errorf("tools/call result %s is missing %s", text, want)
		}
	}
}

// On a method that never carried a body, the declaration states what the method
// already is — so it does nothing at all, in any projection. Taking a body away
// only ever withdraws a promise, which is why it composes with everything.
func TestWithoutBody_OnABodylessMethodChangesNothing(t *testing.T) {
	plain := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Delete(plain, "/v1/flows/:id", launch)
	zip.Get(plain, "/v1/flows", launch)

	declared := zip.New(zip.Config{AppName: "flows", DisableStartupMessage: true})
	zip.Delete(declared, "/v1/flows/:id", launch, zip.WithoutBody())
	zip.Get(declared, "/v1/flows", launch, zip.WithoutBody())

	if a, b := mustJSON(t, plain.OpenAPISpec()), mustJSON(t, declared.OpenAPISpec()); !bytes.Equal(a, b) {
		t.Errorf("document changed:\n plain %s\n declared %s", a, b)
	}
	if a, b := fmt.Sprint(wbNoOp(plain.Commands())), fmt.Sprint(wbNoOp(declared.Commands())); a != b {
		t.Errorf("commands changed:\n plain %s\n declared %s", a, b)
	}
}

// --- helpers, named apart so they cannot collide with another file's ---

func wbOp(t *testing.T, a *zip.App, path, method string) map[string]any {
	t.Helper()
	paths, _ := a.OpenAPISpec()["paths"].(map[string]map[string]any)
	item, ok := paths[path]
	if !ok {
		t.Fatalf("no path %q in the document; paths = %v", path, wbKeys(anyMap2(paths)))
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		t.Fatalf("no %s on %q; have %v", method, path, wbKeys(item))
	}
	return op
}

func wbSchemas(t *testing.T, a *zip.App) map[string]any {
	t.Helper()
	comp, _ := a.OpenAPISpec()["components"].(map[string]any)
	defs, _ := comp["schemas"].(map[string]any)
	return defs
}

func wbBodyRef(t *testing.T, rb map[string]any) string {
	t.Helper()
	content, _ := rb["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, _ := media["schema"].(map[string]any)
	ref, _ := schema["$ref"].(string)
	return ref
}

func wbFlags(c zip.Command) []string {
	var out []string
	for _, f := range c.Flags {
		out = append(out, f.Name)
	}
	return out
}

func wbArgs(c zip.Command) []string {
	var out []string
	for _, a := range c.Args {
		out = append(out, a.Name)
	}
	return out
}

// wbNoOp compares commands by what they say, not by the unexported op pointer
// two apps can never share.
func wbNoOp(cmds []zip.Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, fmt.Sprintf("%s %s %s %v %v %v", c.Method, c.Path, c.Name, c.NoBody, c.Args, c.Flags))
	}
	return out
}

func wbKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
