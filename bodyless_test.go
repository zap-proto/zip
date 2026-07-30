package zip_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// dropIn is a DELETE's input: the URL addresses what is deleted and carries the
// rest of the request beside it.
type dropIn struct {
	ID     string `json:"id"`
	Reason string `json:"reason" validate:"required"`
	Force  bool   `json:"force"`
}

type dropOut struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Force  bool   `json:"force"`
}

func drop(_ context.Context, in *dropIn) (*dropOut, error) {
	return &dropOut{ID: in.ID, Reason: in.Reason, Force: in.Force}, nil
}

func dropApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "drop", DisableStartupMessage: true})
	zip.Describe("DELETE /v1/drop/things/:id", zip.Doc{
		Description: "Drop deletes a thing.",
		Fields:      map[string]string{"dropIn.reason": "Why it is being deleted."},
		Example:     json.RawMessage(`{"id":"t_1","reason":"expired"}`),
	})
	zip.Delete(a, "/v1/drop/things/:id", drop)
	return a
}

// A DELETE carries no body — hasBody said so, the document said so, and the
// route read one anyway. It now reads the URL, which is what every client
// generated from the document sends.
func TestBodyless_DeleteReadsTheURLNotTheBody(t *testing.T) {
	a := dropApp(t)

	code, body := call2(t, a, "DELETE", "/v1/drop/things/t_1?reason=expired&force=true", "")
	if code != 200 {
		t.Fatalf("DELETE with a query = %d %s, want 200", code, body)
	}
	for _, want := range []string{`"id":"t_1"`, `"reason":"expired"`, `"force":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s is missing %s", body, want)
		}
	}

	// And a body is no longer an input. It is not an error to send one — the
	// method simply does not carry one — so the request is refused by the
	// validator for the field it never received, not by the decoder.
	code, body = call2(t, a, "DELETE", "/v1/drop/things/t_1", `{"reason":"smuggled"}`)
	if code != 400 || !strings.Contains(body, "reason") {
		t.Fatalf("DELETE with only a body = %d %s, want 400 naming the missing field", code, body)
	}
}

// The document types a URL-borne field's required-ness from the same
// `validate:"required"` that makes the handler refuse without it. Declaring
// every query parameter optional described a call that cannot succeed, and every
// generated client made the argument optional to match.
func TestBodyless_RequiredReachesTheParameter(t *testing.T) {
	params := paramsOf2(t, dropApp(t), "/v1/drop/things/{id}", "delete")
	want := map[string]bool{"id": true, "reason": true, "force": false}
	if len(params) != len(want) {
		t.Fatalf("parameters = %v, want %d", params, len(want))
	}
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		name, _ := p["name"].(string)
		if p["required"] != want[name] {
			t.Errorf("%q required = %v, want %v", name, p["required"], want[name])
		}
	}
}

// The doc comment's example survives a bodyless op, spread across the parameters
// that carry it — the only place a document has to put it when there is no
// requestBody. Without this, every GET and DELETE reached the published
// reference and the generated CLI with no example at all.
func TestBodyless_ExampleSurvivesAsParameterExamples(t *testing.T) {
	a := dropApp(t)
	got := map[string]string{}
	for _, raw := range paramsOf2(t, a, "/v1/drop/things/{id}", "delete") {
		p, _ := raw.(map[string]any)
		if ex, ok := p["example"]; ok {
			got[p["name"].(string)] = strings.Trim(string(mustJSON(t, ex)), `"`)
		}
	}
	if got["id"] != "t_1" || got["reason"] != "expired" {
		t.Fatalf("parameter examples = %v, want the doc comment's own example", got)
	}
	// `force` was not in the example, so it carries none — an example is what
	// was written, not a value invented per field.
	if _, ok := got["force"]; ok {
		t.Errorf("force carries an example it was never given: %v", got)
	}

	// And it round-trips: the client tree rebuilt from the document renders the
	// same example command line as the one built from the registry.
	spec, err := json.Marshal(a.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	fromSpec, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	if len(fromSpec) != 1 || !sameJSON(fromSpec[0].Example, a.Commands()[0].Example) {
		t.Fatalf("spec example %s, registry example %s", fromSpec[0].Example, a.Commands()[0].Example)
	}
}

// A bodyless op's flags are what the URL can actually carry. `--tags` on a
// DELETE used to marshal fine, go out as a query value and be dropped by the
// binder: a flag the CLI offered and the wire ignored.
func TestBodyless_FlagsAreWhatTheURLCanCarry(t *testing.T) {
	type wideIn struct {
		ID   string   `json:"id"`
		Note string   `json:"note"`
		Tags []string `json:"tags"`
	}
	a := zip.New(zip.Config{AppName: "wide", DisableStartupMessage: true})
	zip.Delete(a, "/v1/wide/things/:id", func(_ context.Context, in *wideIn) (*wideIn, error) { return in, nil })
	zip.Post(a, "/v1/wide/things", func(_ context.Context, in *wideIn) (*wideIn, error) { return in, nil })

	byName := map[string][]string{}
	for _, c := range a.Commands() {
		for _, f := range c.Flags {
			byName[c.Name] = append(byName[c.Name], f.Name)
		}
	}
	if got := byName["things-delete"]; len(got) != 1 || got[0] != "note" {
		t.Errorf("DELETE flags = %v, want just --note — a slice cannot ride a URL", got)
	}
	if got := byName["things-create"]; len(got) != 3 {
		t.Errorf("POST flags = %v, want all three — a body carries anything", got)
	}
}

// verifyIn addresses its target entirely with the URL. There is nothing left for
// a body to carry.
type verifyIn struct {
	ID string `json:"id"`
}

type verifyOut struct {
	Verified bool `json:"verified"`
}

func verifyApp() *zip.App {
	a := zip.New(zip.Config{AppName: "verify", DisableStartupMessage: true})
	zip.Post(a, "/v1/drop/things/:id/verify", func(_ context.Context, in *verifyIn) (*verifyOut, error) {
		return &verifyOut{Verified: in.ID == "t_1"}, nil
	})
	return a
}

// A POST whose input is nothing but its path parameters has no request body, and
// publishing one anyway put a REQUIRED argument into every generated SDK whose
// only properties were the path params the caller already passes. The body was
// never read for anything — bindURL binds the path last — so the phantom was
// only ever in the document.
func TestBodyless_PostWithOnlyPathParamsPublishesNoBody(t *testing.T) {
	a := verifyApp()
	spec := a.OpenAPISpec()
	paths, _ := spec["paths"].(map[string]map[string]any)
	op, _ := paths["/v1/drop/things/{id}/verify"]["post"].(map[string]any)
	if op == nil {
		t.Fatalf("no POST in spec; paths = %v", keysOf(anyMap2(paths)))
	}
	if rb, ok := op["requestBody"]; ok {
		t.Errorf("requestBody = %v, want none — the URL is the whole input", rb)
	}
	// And no query parameters either: every field IS a path param, so there is
	// nothing to declare a second way.
	params, _ := op["parameters"].([]any)
	if len(params) != 1 {
		t.Fatalf("parameters = %v, want just the path param", params)
	}

	// The CLI agrees, from the same registry: one positional, no flags.
	cmds := a.Commands()
	if len(cmds) != 1 || len(cmds[0].Args) != 1 || len(cmds[0].Flags) != 0 {
		t.Errorf("command = %+v, want one arg and no flags", cmds[0])
	}
}

// The WIRE is untouched. POST still carries a body on the wire, so the route
// still reads one and still refuses one it cannot parse. Only the document
// stopped claiming an argument that does nothing.
func TestBodyless_PostStillReadsTheWireItAlwaysDid(t *testing.T) {
	a := verifyApp()
	if code, body := call2(t, a, "POST", "/v1/drop/things/t_1/verify", ""); code != 200 || !strings.Contains(body, `"verified":true`) {
		t.Errorf("POST with no body = %d %s, want 200 verified", code, body)
	}
	// The URL is the addressing authority: a body that repeats a path param
	// never won, which is exactly why removing it from the document is safe.
	if code, body := call2(t, a, "POST", "/v1/drop/things/t_1/verify", `{"id":"t_9"}`); code != 200 || !strings.Contains(body, `"verified":true`) {
		t.Errorf("POST with a smuggled id = %d %s, want the URL's id to win", code, body)
	}
	if code, _ := call2(t, a, "POST", "/v1/drop/things/t_1/verify", `{`); code != 400 {
		t.Errorf("POST with a malformed body = %d, want the 400 it has always answered", code)
	}
}

func paramsOf2(t *testing.T, a *zip.App, path, method string) []any {
	t.Helper()
	paths, _ := a.OpenAPISpec()["paths"].(map[string]map[string]any)
	item, ok := paths[path]
	if !ok {
		t.Fatalf("no path %q in spec; paths = %v", path, keysOf(anyMap2(paths)))
	}
	op, _ := item[method].(map[string]any)
	params, _ := op["parameters"].([]any)
	return params
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
