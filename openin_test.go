package zip_test

// An OPEN input is one whose KEYS BELONG TO THE CALLER: a document store's
// payload, a settings patch, a metrics push — a shape whose keys are data rather
// than schema. The binder walked a struct or returned, so such an op received its
// body and NOTHING from the URL: the path segment the router matched on, the same
// segment the authorizer reads, simply vanished. It could have an open body or an
// addressable resource, never both — which sent every one of those routes back to
// an untyped handler, and out of the document, the tool list and the command line
// with it.
//
// These pin the capability across all five projections, and the lies it replaces:
// the URL binds into an open object under the SAME authority order a struct gets;
// the document publishes an open requestBody, a free-form query and the response
// it actually sends (it used to say "204 no content" while answering 200 with a
// body); the MCP tool schema says any JSON value where it used to promise every
// value was an object; and the CLI carries the whole input in one flag, spelled
// the same whether it was derived from the registry or from the document.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// openEcho hands back exactly the object it received, which is what makes what
// BOUND observable.
func openEcho(_ context.Context, in *map[string]any) (*map[string]any, error) {
	out := map[string]any{}
	for k, v := range *in {
		out[k] = v
	}
	return &out, nil
}

// counters is a NAMED open object that DECLARES its value type: the keys are
// still the caller's, the values are ints.
type counters map[string]int

func countEcho(_ context.Context, in *counters) (*counters, error) {
	out := counters{}
	for k, v := range *in {
		out[k] = v
	}
	return &out, nil
}

// openFilter is a CLOSED input carrying a map FIELD. A field is not a URL target
// — only the top level is — and it must not become one because the root now can be.
type openFilter struct {
	ID   string            `json:"id"`
	Meta map[string]string `json:"meta"`
}

func filterEcho(_ context.Context, in *openFilter) (*openFilter, error) { return in, nil }

func openApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "d", DisableStartupMessage: true})
	zip.Put(a, "/v1/d/docs/:id", openEcho)                   // an open body AND an addressed resource
	zip.Get(a, "/v1/d/docs/:id", openEcho)                   // no body at all: the URL is the whole input
	zip.Post(a, "/v1/d/docs", openEcho, zip.WithStatus(201)) // an open body under a declared status
	zip.Get(a, "/v1/d/counters/:n", countEcho)               // an open object with a declared value type
	zip.Get(a, "/v1/d/filters/:id", filterEcho)              // a closed input with a map field
	return a
}

func openCall(t *testing.T, a *zip.App, method, path, body string) (int, map[string]json.RawMessage) {
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
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: response is not a JSON object: %s", method, path, raw)
		}
	}
	return resp.StatusCode, out
}

func openWant(t *testing.T, got map[string]json.RawMessage, key, want string) {
	t.Helper()
	raw, ok := got[key]
	if !ok {
		t.Errorf("key %q did not bind at all; got %s", key, openJSON(got))
		return
	}
	if string(raw) != want {
		t.Errorf("key %q = %s, want %s", key, raw, want)
	}
}

func openJSON(m map[string]json.RawMessage) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// ---------------------------------------------------------------------------
// The runtime: an open input binds the URL
// ---------------------------------------------------------------------------

// THE GAP. The path segment reaches the handler. It used to vanish: an op with an
// open body received the body and nothing else, so nothing in it said WHICH
// document was being written.
func TestOpenIn_PathBindsIntoAnOpenInput(t *testing.T) {
	code, out := openCall(t, openApp(t), "PUT", "/v1/d/docs/d1", `{"title":"a doc"}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	openWant(t, out, "id", `"d1"`)
	openWant(t, out, "title", `"a doc"`)
}

// The keys are the caller's, so EVERY query key binds — the opposite of a struct,
// where a name no field declares is ignored. That is what open means.
func TestOpenIn_EveryQueryKeyBinds(t *testing.T) {
	code, out := openCall(t, openApp(t), "GET", "/v1/d/docs/d1?tag=draft&utm_source=x", "")
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	openWant(t, out, "id", `"d1"`)
	openWant(t, out, "tag", `"draft"`)
	openWant(t, out, "utm_source", `"x"`)
}

// The authority order is the one a struct gets, because it is the same URL: body,
// then query, then the path the router MATCHED on.
func TestOpenIn_URLIsTheAuthority(t *testing.T) {
	code, out := openCall(t, openApp(t), "PUT", "/v1/d/docs/d1?id=fromquery&note=fromquery",
		`{"id":"frombody","note":"frombody","title":"kept"}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	openWant(t, out, "id", `"d1"`)          // the routed segment wins
	openWant(t, out, "note", `"fromquery"`) // the query beats the body
	openWant(t, out, "title", `"kept"`)     // a body-only key still binds
}

// THE SECURITY PROPERTY, for an open input: the authorizer runs on this same
// decoded value, so if the body could override the path it would approve one
// document and the handler would write another.
func TestOpenIn_AuthorizerSeesThePathTarget(t *testing.T) {
	a := zip.New(zip.Config{AppName: "d", DisableStartupMessage: true})
	var saw string
	a.Authorize(func(_ context.Context, _ zip.Op, in any) error {
		if m, ok := in.(*map[string]any); ok && m != nil {
			if v, ok := (*m)["id"].(string); ok {
				saw = v
			}
		}
		return nil
	})
	zip.Put(a, "/v1/d/docs/:id", openEcho)
	if code, _ := openCall(t, a, "PUT", "/v1/d/docs/d1", `{"id":"victim"}`); code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if saw != "d1" {
		t.Fatalf("authorizer saw id = %q, want %q — the routed path must win", saw, "d1")
	}
}

// A value stays TEXT where the type says nothing about it: an open value has no
// kind to convert to, and guessing one would make the same key arrive as a number
// or a string depending on what a caller happened to type.
func TestOpenIn_ValuesStayTextWhenTheTypeIsOpen(t *testing.T) {
	_, out := openCall(t, openApp(t), "GET", "/v1/d/docs/d1?n=5&flag=true", "")
	openWant(t, out, "n", `"5"`)
	openWant(t, out, "flag", `"true"`)
}

// Where the map DOES declare its value type, that declaration is honoured —
// through the same setScalar every struct field runs through. A value the type
// cannot hold writes no key at all, which is what "nothing arrived" looks like in
// a map (a struct field has to sit at its zero instead, because it exists either way).
func TestOpenIn_DeclaredValueTypeIsHonoured(t *testing.T) {
	code, out := openCall(t, openApp(t), "GET", "/v1/d/counters/3?hits=7&bad=abc", "")
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	openWant(t, out, "n", `3`)
	openWant(t, out, "hits", `7`)
	if raw, ok := out["bad"]; ok {
		t.Errorf("bad = %s, want no key at all — an int map cannot hold %q", raw, "abc")
	}
}

// An op that reads no body still receives its path parameters: the nil map is
// allocated on the first write.
func TestOpenIn_NilInputIsAllocatedForTheURL(t *testing.T) {
	code, out := openCall(t, openApp(t), "GET", "/v1/d/docs/d1", "")
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(out) != 1 {
		t.Fatalf("out = %s, want just the path parameter", openJSON(out))
	}
	openWant(t, out, "id", `"d1"`)
}

// With nothing to write it stays nil, which reads like any other nil map — and
// the declared status still governs.
func TestOpenIn_EmptyOpenInputIsHarmless(t *testing.T) {
	code, out := openCall(t, openApp(t), "POST", "/v1/d/docs", "")
	if code != 201 {
		t.Fatalf("status = %d, want the declared 201", code)
	}
	if len(out) != 0 {
		t.Fatalf("out = %s, want an empty object", openJSON(out))
	}
}

// KEEPS THE OLD PIN'S INTENT (queryparam_test's TestTypedGetLeavesNonScalarsUnbound
// and bindURL's "only the top level is walked"): a map FIELD of a closed struct is
// still not a URL target. The ROOT being open is a property of the input as a
// whole; it does not make every nested map addressable by a query key.
func TestOpenIn_AMapFieldIsStillNotAURLTarget(t *testing.T) {
	code, out := openCall(t, openApp(t), "GET", "/v1/d/filters/f1?meta=x&meta.k=v", "")
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
	openWant(t, out, "id", `"f1"`)
	if raw, ok := out["meta"]; ok && string(raw) != "null" {
		t.Errorf("meta = %s, want null — a map field is not URL-bindable", raw)
	}
}

// ---------------------------------------------------------------------------
// The document
// ---------------------------------------------------------------------------

// openSpec is the served document, round-tripped through JSON so what is asserted
// is what a client reads rather than the Go values behind it.
func openSpec(t *testing.T, a *zip.App) map[string]any {
	t.Helper()
	raw, err := json.Marshal(a.OpenAPISpec())
	if err != nil {
		t.Fatalf("the document does not marshal: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("the document is not JSON: %v", err)
	}
	return spec
}

func openDig(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := m
	for i, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("no object at %q (step %d of %v); have %v", k, i+1, keys, openKeys(cur))
		}
		cur = next
	}
	return cur
}

func openKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func openParams(t *testing.T, spec map[string]any, path, method string) map[string]map[string]any {
	t.Helper()
	op := openDig(t, spec, "paths", path, method)
	raw, _ := op["parameters"].([]any)
	out := map[string]map[string]any{}
	for _, r := range raw {
		p, _ := r.(map[string]any)
		name, _ := p["name"].(string)
		out[name] = p
	}
	return out
}

// An open input HAS a body, so the document says so. It used to publish no
// requestBody at all — describing an op that takes nothing while the route read a
// whole document, so every generated SDK dropped the argument.
func TestOpenIn_DocumentPublishesAnOpenBody(t *testing.T) {
	spec := openSpec(t, openApp(t))
	body := openDig(t, spec, "paths", "/v1/d/docs/{id}", "put", "requestBody")
	if body["required"] != true {
		t.Errorf("requestBody.required = %v, want true", body["required"])
	}
	schema := openDig(t, body, "content", "application/json", "schema")
	if schema["type"] != "object" {
		t.Errorf("schema.type = %v, want object", schema["type"])
	}
	if schema["additionalProperties"] != true {
		t.Errorf("schema.additionalProperties = %v, want true — the keys are the caller's", schema["additionalProperties"])
	}
	if _, closed := schema["properties"]; closed {
		t.Errorf("schema declares properties %v; an open object has none to declare", schema["properties"])
	}
}

// The response is the one the op SENDS. An open Out has no Go name, and the
// document keyed on the name: an op answering `{"a":1}` published "204 no
// content", so every client generated from it expected an empty response.
func TestOpenIn_DocumentPublishesTheOpenResponse(t *testing.T) {
	spec := openSpec(t, openApp(t))
	resp := openDig(t, spec, "paths", "/v1/d/docs/{id}", "put", "responses")
	if _, lie := resp["204"]; lie {
		t.Errorf("responses = %v, want no 204 — this op answers with a body", openKeys(resp))
	}
	schema := openDig(t, resp, "200", "content", "application/json", "schema")
	if schema["type"] != "object" || schema["additionalProperties"] != true {
		t.Errorf("200 schema = %v, want an open object", schema)
	}
	// And a declared status keys the same response, for an open Out as for a named one.
	created := openDig(t, spec, "paths", "/v1/d/docs", "post", "responses")
	if _, ok := created["201"]; !ok {
		t.Errorf("responses = %v, want the declared 201", openKeys(created))
	}
	openDig(t, created, "201", "content", "application/json", "schema")
}

// A bodyless op reads its whole input from the URL. An open input declares no
// fields to list, and its keys are the caller's, so the document says exactly
// that: one free-form form-style parameter, which is how OpenAPI spells a query
// whose names are not known in advance. Without it the document described an op
// that reads the whole URL as taking nothing from it.
func TestOpenIn_DocumentPublishesTheFreeFormQuery(t *testing.T) {
	spec := openSpec(t, openApp(t))
	params := openParams(t, spec, "/v1/d/docs/{id}", "get")

	id, ok := params["id"]
	if !ok {
		t.Fatalf("path parameter id missing; parameters = %v", params)
	}
	if id["in"] != "path" || id["required"] != true {
		t.Errorf("id = %v, want a required path parameter", id)
	}
	// A parameter's TYPE is the type of the value it binds to. An open input has no
	// field to read it off, so it comes from the map's value type — `any` carries
	// the URL's text, so a string.
	if s, _ := id["schema"].(map[string]any); s["type"] != "string" {
		t.Errorf("id.schema = %v, want string", s)
	}
	// ...and where the value type IS declared, the document says THAT, rather than
	// calling text what the binder writes as a number.
	n := openParams(t, spec, "/v1/d/counters/{n}", "get")["n"]
	if s, _ := n["schema"].(map[string]any); s["type"] != "integer" {
		t.Errorf("counters n.schema = %v, want integer — the map declares int values", s)
	}

	q, ok := params[zipInputFlag]
	if !ok {
		t.Fatalf("no free-form query parameter; parameters = %v", params)
	}
	if q["in"] != "query" {
		t.Errorf("%s.in = %v, want query", zipInputFlag, q["in"])
	}
	if q["required"] != false {
		t.Errorf("%s.required = %v, want false", zipInputFlag, q["required"])
	}
	// form + explode is what serializes one object as `?key=value&key2=value2`,
	// which is exactly what the binder reads back.
	if q["style"] != "form" || q["explode"] != true {
		t.Errorf("%s = %v, want style form, explode true", zipInputFlag, q)
	}
	schema, _ := q["schema"].(map[string]any)
	if schema["type"] != "object" || schema["additionalProperties"] != true {
		t.Errorf("%s.schema = %v, want an open object", zipInputFlag, schema)
	}

	// A body method carries the object in its requestBody, so it does NOT also
	// declare the query half — one input, one place, exactly as for a struct.
	if _, dup := openParams(t, spec, "/v1/d/docs/{id}", "put")[zipInputFlag]; dup {
		t.Errorf("a body method declared the free-form query as well as a requestBody")
	}
}

// zipInputFlag mirrors the unexported constant: one name, asserted from outside
// the package the way a caller reads it.
const zipInputFlag = "input"

// ---------------------------------------------------------------------------
// The MCP tool
// ---------------------------------------------------------------------------

// The tool schema is the same derivation, so it is open too — and `any` is spelled
// `true`, not {"type":"object"}: a client validating a call against the old schema
// refused the numbers, strings and arrays the map actually carries.
func TestOpenIn_MCPToolSchemaIsOpen(t *testing.T) {
	var tool map[string]any
	for _, tl := range openApp(t).MCPTools() {
		if tl["name"] == "put_v1_d_docs_id" {
			tool = tl
		}
	}
	if tool == nil {
		t.Fatalf("tool put_v1_d_docs_id not listed")
	}
	schema, _ := tool["inputSchema"].(map[string]any)
	if schema["type"] != "object" {
		t.Errorf("inputSchema.type = %v, want object", schema["type"])
	}
	if ap, ok := schema["additionalProperties"].(bool); !ok || !ap {
		t.Errorf("inputSchema.additionalProperties = %v, want true", schema["additionalProperties"])
	}
}

// ...and the call the schema promises works: a tools/call carries the whole object,
// mixed value types and all. No URL is involved, so nothing binds over it.
func TestOpenIn_MCPCallCarriesTheOpenObject(t *testing.T) {
	a := openApp(t)
	a.Prepare()
	req, _ := http.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"put_v1_d_docs_id",`+
			`"arguments":{"id":"d1","n":1,"tags":["a"],"nested":{"k":"v"}}}}`))
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
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &out); err != nil {
		t.Fatalf("tool result is not an object: %s", env.Result.Content[0].Text)
	}
	openWant(t, out, "id", `"d1"`)
	openWant(t, out, "n", `1`)
	openWant(t, out, "tags", `["a"]`)
	openWant(t, out, "nested", `{"k":"v"}`)
}

// ---------------------------------------------------------------------------
// The command line
// ---------------------------------------------------------------------------

func openCmd(t *testing.T, cmds []zip.Command, id string) zip.Command {
	t.Helper()
	for _, c := range cmds {
		if c.OperationID == id {
			return c
		}
	}
	t.Fatalf("no command for %q; have %v", id, openIDs(cmds))
	return zip.Command{}
}

func openIDs(cmds []zip.Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.OperationID)
	}
	return out
}

// An open input has no declared keys to spell one flag each after, so the one
// flag IS the value. Offering none at all — what a non-struct input used to get —
// left the command unable to say anything the handler would read.
func TestOpenIn_CommandCarriesTheWholeInput(t *testing.T) {
	cmd := openCmd(t, openApp(t).Commands(), "put_v1_d_docs_id")
	if len(cmd.Args) != 1 || cmd.Args[0].Name != "id" {
		t.Fatalf("args = %+v, want the path parameter positional", cmd.Args)
	}
	if len(cmd.Flags) != 1 {
		t.Fatalf("flags = %+v, want exactly one", cmd.Flags)
	}
	f := cmd.Flags[0]
	if f.Name != zipInputFlag || f.Type != "json" || f.Field != "" {
		t.Errorf("flag = %+v, want --%s json carrying the whole input (empty Field)", f, zipInputFlag)
	}
}

// End to end through the op's own invoke seam: the flag's value IS the input, and
// the path argument still overrules what it says about the target.
func TestOpenIn_CLIRunsAnOpenInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"a body method", []string{"d", "docs-update", "d1", "--input", `{"id":"victim","title":"x"}`}},
		{"a bodyless method", []string{"d", "docs-get", "d1", "--input", `{"title":"x"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			cli := openApp(t).CLI()
			cli.Out = &out
			if err := cli.Run(context.Background(), tc.argv); err != nil {
				t.Fatalf("%v: %v", tc.argv, err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
				t.Fatalf("output is not an object: %s", out.String())
			}
			openWant(t, got, "id", `"d1"`)
			openWant(t, got, "title", `"x"`)
		})
	}
}

// A flag whose value is not JSON is refused by the flag, not by the handler.
func TestOpenIn_CLIRefusesAnInputThatIsNotJSON(t *testing.T) {
	var out strings.Builder
	cli := openApp(t).CLI()
	cli.Out = &out
	err := cli.Run(context.Background(), []string{"d", "docs-update", "d1", "--input", "nope"})
	if err == nil {
		t.Fatalf("a non-JSON --%s was accepted", zipInputFlag)
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
}

// The example a reader copies is the one the document ships: an open input has no
// per-key flags to split it across, so it rides the one flag whole.
func TestOpenIn_CLIExampleCarriesTheWholeInput(t *testing.T) {
	a := zip.New(zip.Config{AppName: "d", DisableStartupMessage: true})
	zip.Describe("PUT /v1/d/docs/:id", zip.Doc{
		Description: "Write one document.",
		Example:     json.RawMessage(`{"id":"d1","title":"x"}`),
	})
	zip.Put(a, "/v1/d/docs/:id", openEcho)
	var out strings.Builder
	cli := a.CLI()
	cli.Out = &out
	if err := cli.Run(context.Background(), []string{"d", "docs-update", "--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--"+zipInputFlag) || !strings.Contains(out.String(), "title") {
		t.Errorf("help does not show the example input:\n%s", out.String())
	}
}

// The two derivations are ONE derivation: a command read off the live registry and
// a command read off the document that registry generates must be the same
// command. additionalProperties is what carries "the keys are the caller's" over
// the wire — a client that links nothing of the service has only the document.
func TestOpenIn_SpecAndRegistryAgreeOnAnOpenInput(t *testing.T) {
	a := openApp(t)
	spec, err := json.Marshal(a.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	fromSpec, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	registry := a.Commands()
	if len(fromSpec) != len(registry) {
		t.Fatalf("spec has %v, registry has %v", openIDs(fromSpec), openIDs(registry))
	}
	for _, want := range registry {
		got := openCmd(t, fromSpec, want.OperationID)
		if len(got.Args) != len(want.Args) {
			t.Errorf("%s: args %+v, want %+v", want.OperationID, got.Args, want.Args)
		}
		if len(got.Flags) != len(want.Flags) {
			t.Fatalf("%s: flags %+v, want %+v", want.OperationID, got.Flags, want.Flags)
		}
		for i, w := range want.Flags {
			g := got.Flags[i]
			if g.Name != w.Name || g.Field != w.Field || g.Type != w.Type || g.Required != w.Required {
				t.Errorf("%s: flag %d = %+v, want %+v", want.OperationID, i, g, w)
			}
		}
	}
}

// THE BOUNDARY, stated rather than discovered: the by-name call plane carries ZAP,
// which describes a message by its layout, and an open object has none. So a call
// is REFUSED at encode — before anything reaches the wire — instead of arriving
// with the caller's keys quietly missing. An open input is a REST, MCP and command
// line capability; a sibling service that wants one has a URL to send it to.
func TestOpenIn_TheCallPlaneRefusesAnOpenInputLoudly(t *testing.T) {
	c, err := zip.Dial(t.TempDir() + "/never-listens.sock")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	in := map[string]any{"id": "d1"}
	// No server is listening: reaching the network at all would be the bug this
	// asserts against, since the refusal must happen at encode.
	if _, err := zip.Call[map[string]any, map[string]any](context.Background(), c, "put_v1_d_docs_id", &in); err == nil {
		t.Fatal("an open input crossed the call plane; keys would be silently dropped")
	} else if !strings.Contains(err.Error(), "encode") {
		t.Errorf("error = %v, want it refused at encode", err)
	}
}

// An EMPTY object is not an open one: a struct with no fields declares that it
// accepts nothing, and takes no flags. Only additionalProperties says the keys
// belong to the caller.
func TestOpenIn_AnEmptyObjectIsNotAnOpenOne(t *testing.T) {
	a := zip.New(zip.Config{AppName: "e", DisableStartupMessage: true})
	type emptyIn struct{}
	zip.Post(a, "/v1/e/ping", func(_ context.Context, _ *emptyIn) (*emptyIn, error) { return &emptyIn{}, nil })
	spec, err := json.Marshal(a.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	fromSpec, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	for _, side := range []struct {
		from  string
		flags []zip.Flag
	}{
		{"the registry", a.Commands()[0].Flags},
		{"the document", fromSpec[0].Flags},
	} {
		if len(side.flags) != 0 {
			t.Errorf("%s: flags = %+v, want none", side.from, side.flags)
		}
	}
}
