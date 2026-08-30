package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A refusal is an ANSWER, and an answer has a body. zip's own is the envelope —
// {status, code, error} — and a service whose refusal carries STRUCTURE had
// nowhere to put it: the envelope has one string, so the shape a caller has to
// react to arrived as prose inside it, and the document said nothing about the
// status at all. The only alternative was to leave the route untyped and lose
// the document, the tool list, the command and the call plane together.
//
// These tests pin both halves: the declared body is what the wire carries, and
// the declaration is what every projection publishes.

// blockedBody is the guide's 409: a flat body whose keys are the service's own,
// with no room for a second error object around them.
type blockedBody struct {
	Error     string `json:"error"`
	Step      int    `json:"step"`
	BlockedBy string `json:"blockedBy"`
}

// denyBody is the shape a payment refusal carries on every Hanzo surface: one
// error object with a code and a message — and the SAME shape at 402 and at 503,
// which is why a fault's status cannot live in its type.
type denyBody struct {
	Error denyReason `json:"error"`
}

type denyReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type stepRunIn struct {
	ID string `json:"id"`
}

type stepRunOut struct {
	ID  string `json:"id"`
	Ran bool   `json:"ran"`
}

// runStep answers three ways: the success, the DECLARED refusal, and the plain
// envelope a handler has always been able to return.
func runStep(_ context.Context, in *stepRunIn) (*stepRunOut, error) {
	switch in.ID {
	case "blocked":
		return nil, zip.ErrConflict("step 2 is blocked").
			WithBody(blockedBody{Error: "step 2 is blocked", Step: 2, BlockedBy: "deploy"})
	case "flat":
		return nil, zip.ErrConflict("step 2 is blocked")
	}
	return &stepRunOut{ID: in.ID, Ran: true}, nil
}

func faultApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "guide", DisableStartupMessage: true})
	zip.Post(a, "/v1/guide/steps/:id/run", runStep,
		zip.WithOperationID("guide_step_run"), zip.WithFault[blockedBody](409))
	return a
}

// faultCall issues one in-memory request and reports the status, the content
// type and the body — the content type matters because a declared body must go
// out as JSON like every other body, not as bytes.
func faultCall(t *testing.T, a *zip.App, method, path, body string) (int, string, string) {
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
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(raw)
}

// faultResponses reads one op's whole responses object out of the document.
func faultResponses(t *testing.T, a *zip.App, path, method string) map[string]any {
	t.Helper()
	spec := a.OpenAPISpec()
	paths, _ := spec["paths"].(map[string]map[string]any)
	item, ok := paths[path]
	if !ok {
		t.Fatalf("no path %q in the document", path)
	}
	op, _ := item[method].(map[string]any)
	resp, _ := op["responses"].(map[string]any)
	if resp == nil {
		t.Fatalf("%s %s declares no responses", method, path)
	}
	return resp
}

// schemaRef is the $ref one response's JSON media points at, and the definition
// it names — the pair a generated SDK reads to build a typed error.
func faultSchemaRef(t *testing.T, a *zip.App, resp map[string]any, code string) (string, map[string]any) {
	t.Helper()
	entry, ok := resp[code].(map[string]any)
	if !ok {
		t.Fatalf("no %s response; responses = %v", code, faultKeys(resp))
	}
	content, _ := entry["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, _ := media["schema"].(map[string]any)
	if schema == nil {
		t.Fatalf("%s response carries no schema: %v", code, entry)
	}
	ref, _ := schema["$ref"].(string)
	if ref == "" {
		return "", schema // inline: an unnamed shape describes itself
	}
	name := strings.TrimPrefix(ref, "#/components/schemas/")
	comps, _ := a.OpenAPISpec()["components"].(map[string]any)
	defs, _ := comps["schemas"].(map[string]any)
	def, _ := defs[name].(map[string]any)
	if def == nil {
		t.Fatalf("%s response $refs %q, which the document does not define", code, ref)
	}
	return name, def
}

func faultKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func faultProps(t *testing.T, def map[string]any) map[string]any {
	t.Helper()
	props, _ := def["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("definition has no properties: %v", def)
	}
	return props
}

// The declared body is the WHOLE body. Not a field of the envelope, not wrapped
// in one: the bytes on the wire are the shape the service publishes, which is
// the only way a route whose error contract is already fixed can be typed at all.
func TestFault_DeclaredBodyIsTheWholeBody(t *testing.T) {
	a := faultApp(t)

	code, ctype, body := faultCall(t, a, "POST", "/v1/guide/steps/blocked/run", `{}`)
	if code != 409 {
		t.Fatalf("status = %d, want the 409 the handler returned", code)
	}
	if want := `{"error":"step 2 is blocked","step":2,"blockedBy":"deploy"}`; strings.TrimSpace(body) != want {
		t.Fatalf("body = %s\nwant %s", body, want)
	}
	if strings.Contains(body, `"status"`) {
		t.Errorf("body = %s, want no envelope around the declared shape", body)
	}
	if !strings.HasPrefix(ctype, "application/json") {
		t.Errorf("content type = %q, want JSON like every other body", ctype)
	}
	// The success answer is untouched by any of this.
	if code, _, body := faultCall(t, a, "POST", "/v1/guide/steps/s1/run", `{}`); code != 200 ||
		!strings.Contains(body, `"ran":true`) {
		t.Fatalf("success answer = %d %s, want 200 and the Out", code, body)
	}
}

// The failure it replaces, pinned twice: WITHOUT a declaration the structure has
// only the envelope's one string to live in, and the document says nothing about
// the status. An op that declares nothing is byte-for-byte what it always was.
func TestFault_WithoutADeclarationNothingChanges(t *testing.T) {
	a := faultApp(t)

	_, _, body := faultCall(t, a, "POST", "/v1/guide/steps/flat/run", `{}`)
	if want := `{"status":409,"error":"step 2 is blocked"}`; strings.TrimSpace(body) != want {
		t.Fatalf("undeclared refusal body = %s\nwant the envelope %s", body, want)
	}

	plain := zip.New(zip.Config{AppName: "guide", DisableStartupMessage: true})
	zip.Post(plain, "/v1/guide/steps/:id/run", runStep)
	resp := faultResponses(t, plain, "/v1/guide/steps/{id}/run", "post")
	if len(resp) != 1 || resp["200"] == nil {
		t.Fatalf("an op declaring no fault documents %v, want only its success status", faultKeys(resp))
	}
}

// …and the declaration reaches the DOCUMENT, which is the whole point: a
// generated SDK gets a typed 409 it can react to, instead of reading a
// deliberate refusal as an unexpected error.
func TestFault_ReachesTheDocument(t *testing.T) {
	a := faultApp(t)
	resp := faultResponses(t, a, "/v1/guide/steps/{id}/run", "post")

	if resp["200"] == nil {
		t.Errorf("the success response was lost; responses = %v", faultKeys(resp))
	}
	entry, _ := resp["409"].(map[string]any)
	if entry == nil {
		t.Fatalf("no 409 response; responses = %v", faultKeys(resp))
	}
	if entry["description"] != "conflict" {
		t.Errorf("409 description = %v, want the reason phrase", entry["description"])
	}
	name, def := faultSchemaRef(t, a, resp, "409")
	if name != "blockedBody" {
		t.Fatalf("409 schema names %q, want the declared type", name)
	}
	props := faultProps(t, def)
	for _, f := range []string{"error", "step", "blockedBy"} {
		if props[f] == nil {
			t.Errorf("blockedBody publishes no %q; properties = %v", f, faultKeys(props))
		}
	}
	if step, _ := props["step"].(map[string]any); step["type"] != "integer" {
		t.Errorf("step publishes %v, want integer — the shape is derived, not restated", step)
	}
}

// One shape, two statuses. A refusal's status cannot live in its type: the
// denial body a payment gate sends is the same at 402 and at 503, and both are
// part of the contract.
func TestFault_OneShapeAtTwoStatuses(t *testing.T) {
	a := zip.New(zip.Config{AppName: "company", DisableStartupMessage: true})
	zip.Post(a, "/v1/company/payment", func(_ context.Context, in *stepRunIn) (*stepRunOut, error) {
		switch in.ID {
		case "broke":
			return nil, (&zip.HTTPError{Status: 402, Code: "insufficient_balance", Msg: "insufficient balance"}).
				WithBody(denyBody{Error: denyReason{Code: "insufficient_balance", Message: "Add credits at console.hanzo.ai"}})
		case "down":
			return nil, (&zip.HTTPError{Status: 503, Code: "balance_unavailable", Msg: "billing unavailable"}).
				WithBody(denyBody{Error: denyReason{Code: "balance_unavailable", Message: "Billing temporarily unavailable"}})
		}
		return &stepRunOut{ID: in.ID, Ran: true}, nil
	}, zip.WithFault[denyBody](402), zip.WithFault[denyBody](503))

	for _, tc := range []struct {
		id, want string
		code     int
	}{
		{"broke", `{"error":{"code":"insufficient_balance","message":"Add credits at console.hanzo.ai"}}`, 402},
		{"down", `{"error":{"code":"balance_unavailable","message":"Billing temporarily unavailable"}}`, 503},
	} {
		code, _, body := faultCall(t, a, "POST", "/v1/company/payment", `{"id":"`+tc.id+`"}`)
		if code != tc.code || strings.TrimSpace(body) != tc.want {
			t.Fatalf("%s answered %d %s\nwant %d %s", tc.id, code, body, tc.code, tc.want)
		}
	}

	resp := faultResponses(t, a, "/v1/company/payment", "post")
	n402, _ := faultSchemaRef(t, a, resp, "402")
	n503, _ := faultSchemaRef(t, a, resp, "503")
	if n402 != "denyBody" || n503 != "denyBody" {
		t.Fatalf("402 → %q, 503 → %q, want one definition named by both", n402, n503)
	}
	if resp["503"].(map[string]any)["description"] != "service unavailable" {
		t.Errorf("503 description = %v", resp["503"].(map[string]any)["description"])
	}
}

// A fault declared without a shape publishes zip's OWN envelope — derived from
// the struct that writes it, so the document cannot drift from the renderer. The
// unexported body field is not a key of it: what a reader sees is what a reader
// gets.
func TestFault_AnyDeclaresTheEnvelope(t *testing.T) {
	a := zip.New(zip.Config{AppName: "guide", DisableStartupMessage: true})
	zip.Post(a, "/v1/guide/steps/:id/run", func(_ context.Context, in *stepRunIn) (*stepRunOut, error) {
		return nil, zip.ErrNotFound("no such step")
	}, zip.WithFault[any](404))

	resp := faultResponses(t, a, "/v1/guide/steps/{id}/run", "post")
	name, def := faultSchemaRef(t, a, resp, "404")
	if name != "HTTPError" {
		t.Fatalf("404 schema names %q, want zip's envelope", name)
	}
	props := faultProps(t, def)
	for _, f := range []string{"status", "code", "error"} {
		if props[f] == nil {
			t.Errorf("the envelope publishes no %q; properties = %v", f, faultKeys(props))
		}
	}
	if props["body"] != nil {
		t.Errorf("the envelope publishes a %q key it never writes: %v", "body", faultKeys(props))
	}
	// …and that IS what the route sends.
	code, _, body := faultCall(t, a, "POST", "/v1/guide/steps/x/run", `{}`)
	if code != 404 || strings.TrimSpace(body) != `{"status":404,"error":"no such step"}` {
		t.Fatalf("wire = %d %s, want the envelope the document declared", code, body)
	}
}

// A fault is a REFUSAL, refused at declaration when it is not one — and one
// status carries one schema, so declaring it twice on an op is a document that
// cannot be true.
func TestFault_DeclarationRefusesWhatIsNotARefusal(t *testing.T) {
	for _, code := range []int{200, 201, 302, 399, 600} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("WithFault(%d) was accepted; only a 4xx/5xx refusal is one", code)
				}
			}()
			zip.WithFault[blockedBody](code)
		}()
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("two shapes under one status were accepted; a response carries one schema")
			}
		}()
		a := zip.New(zip.Config{AppName: "guide", DisableStartupMessage: true})
		zip.Post(a, "/v1/guide/dup", runStep, zip.WithFault[blockedBody](409), zip.WithFault[denyBody](409))
	}()
}

// The refusal crosses the op-call plane WHOLE: a sibling service reads the same
// bytes a browser reads, so the caller reacts to the shape rather than parsing
// the message. Before this the plane carried a status and a sentence.
func TestFault_CrossesTheOpCallPlane(t *testing.T) {
	sock := serveUDS(t, faultApp(t))
	c, err := zip.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = zip.Call[stepRunIn, stepRunOut](context.Background(), c, "guide_step_run", &stepRunIn{ID: "blocked"})
	if err == nil {
		t.Fatal("blocked step: want the refusal")
	}
	var he *zip.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("refusal did not cross as an HTTPError: %#v", err)
	}
	if he.Status != 409 || he.Msg != "step 2 is blocked" {
		t.Fatalf("status/message lost: %d %q", he.Status, he.Msg)
	}
	want := `{"error":"step 2 is blocked","step":2,"blockedBy":"deploy"}`
	if string(he.Body()) != want {
		t.Fatalf("body crossed as %s\nwant %s", he.Body(), want)
	}
	// …and it decodes into the very type the callee declared.
	var got blockedBody
	if jerr := json.Unmarshal(he.Body(), &got); jerr != nil {
		t.Fatalf("declared body does not decode: %v", jerr)
	}
	if got.Step != 2 || got.BlockedBy != "deploy" {
		t.Fatalf("decoded %+v, want the shape the handler chose", got)
	}

	// A refusal with no declared body still crosses as it always did.
	_, err = zip.Call[stepRunIn, stepRunOut](context.Background(), c, "guide_step_run", &stepRunIn{ID: "flat"})
	if !errors.As(err, &he) || he.Status != 409 || len(he.Body()) != 0 {
		t.Fatalf("undeclared refusal crossed as %#v, want the envelope and no body", err)
	}
}

// The MCP projection has no response object to key a schema on, so the shape
// reaches an agent where an agent reads it: in the tool result. A model that can
// see WHICH step blocked can act on it; one handed prose can only repeat it.
func TestFault_MCPToolResultCarriesTheShape(t *testing.T) {
	a := faultApp(t)
	a.Prepare()

	code, _, body := faultCall(t, a, "POST", "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"guide_step_run","arguments":{"id":"blocked"}}}`)
	if code != 200 {
		t.Fatalf("mcp status = %d, want 200 — JSON-RPC carries its own outcome", code)
	}
	var res struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("mcp body not json: %v — %s", err, body)
	}
	if !res.Result.IsError || len(res.Result.Content) == 0 {
		t.Fatalf("mcp result = %+v, want an error result", res.Result)
	}
	text := res.Result.Content[0].Text
	if !strings.Contains(text, `"blockedBy":"deploy"`) || !strings.Contains(text, "step 2 is blocked") {
		t.Fatalf("mcp error text = %q, want the refusal's own shape", text)
	}
}

// The CLI is the fourth projection, and it reports a refusal by printing it. A
// declared body is part of that: a command that answered "conflict" while the
// service said which step blocked would be hiding the answer.
func TestFault_CLIReportsTheShape(t *testing.T) {
	a := faultApp(t)
	cli := a.CLI()
	var out bytes.Buffer
	cli.Out = &out

	err := cli.Run(context.Background(), []string{"guide", "step-run", "blocked"})
	if err == nil {
		t.Fatal("blocked step: want the refusal from the command")
	}
	if !strings.Contains(err.Error(), `"blockedBy":"deploy"`) {
		t.Fatalf("command error = %q, want the declared body", err.Error())
	}

	// …and the OTHER derivation still reads the document a fault now adds to: a
	// client CLI built off the wire is the same command tree, responses included.
	spec, jerr := json.Marshal(a.OpenAPISpec())
	if jerr != nil {
		t.Fatalf("marshal spec: %v", jerr)
	}
	cmds, serr := zip.CommandsFromSpec(spec)
	if serr != nil {
		t.Fatalf("a document carrying a declared fault is unreadable: %v", serr)
	}
	if len(cmds) != 1 || cmds[0].Service != "guide" || cmds[0].Name != "step-run" {
		t.Fatalf("spec-derived commands = %+v, want the same one command", cmds)
	}
}

// A refusal is written in more than one place. A service mounted under an outer
// error-flattening filter writes its own status IN BAND — c.JSON(he.Status, he) —
// rather than propagating the error, and a body only zip's renderer honored would
// vanish on exactly those routes. It is the VALUE that decides, so both agree.
func TestFault_AnyWriterSendsTheDeclaredBody(t *testing.T) {
	refusal := zip.ErrConflict("step 2 is blocked").
		WithBody(blockedBody{Error: "step 2 is blocked", Step: 2, BlockedBy: "deploy"})
	want := `{"error":"step 2 is blocked","step":2,"blockedBy":"deploy"}`

	// Marshalled straight — a log, a wrapper, a value that carries it.
	b, err := json.Marshal(refusal)
	if err != nil {
		t.Fatalf("marshal the refusal: %v", err)
	}
	if string(b) != want {
		t.Fatalf("json.Marshal = %s\nwant %s", b, want)
	}

	// …and written in band by a handler under an outer filter.
	a := zip.New(zip.Config{AppName: "guide", DisableStartupMessage: true})
	a.Post("/v1/guide/terminal", func(c *zip.Ctx) error { return c.JSON(refusal.Status, refusal) })
	if code, _, body := faultCall(t, a, "POST", "/v1/guide/terminal", `{}`); code != 409 ||
		strings.TrimSpace(body) != want {
		t.Fatalf("in-band write = %d %s\nwant 409 %s", code, body, want)
	}

	// An error with no body encodes exactly as it always did.
	plain, err := json.Marshal(zip.ErrConflict("step 2 is blocked"))
	if err != nil {
		t.Fatalf("marshal a plain refusal: %v", err)
	}
	if string(plain) != `{"status":409,"error":"step 2 is blocked"}` {
		t.Fatalf("plain refusal encodes as %s, want the envelope unchanged", plain)
	}
}

// An error with no body reads exactly as it always has, and decorating one is a
// COPY: a package-level sentinel answers this request without being rewritten
// for every other request that shares it.
func TestFault_TextAndCopySemantics(t *testing.T) {
	plain := zip.ErrConflict("step 2 is blocked")
	if plain.Error() != "step 2 is blocked" {
		t.Fatalf("Error() = %q, want the message unchanged", plain.Error())
	}
	if plain.Body() != nil {
		t.Fatalf("a plain refusal carries a body: %s", plain.Body())
	}

	withBody := plain.WithBody(blockedBody{Error: "step 2 is blocked", Step: 2, BlockedBy: "deploy"})
	if !strings.Contains(withBody.Error(), `"step":2`) {
		t.Errorf("Error() = %q, want the body a log and a CLI can read", withBody.Error())
	}
	if withBody.Status != 409 || withBody.Msg != "step 2 is blocked" {
		t.Errorf("the copy changed the refusal: %d %q", withBody.Status, withBody.Msg)
	}
	if plain.Error() != "step 2 is blocked" || plain.Body() != nil {
		t.Errorf("decorating mutated the original: %q / %s", plain.Error(), plain.Body())
	}
}

// A body that cannot be encoded leaves the refusal exactly as it was — the
// caller still gets the 409 it earned, with the envelope, rather than a 500
// about a channel.
func TestFault_UnencodableBodyLeavesTheRefusalIntact(t *testing.T) {
	a := zip.New(zip.Config{AppName: "guide", DisableStartupMessage: true})
	zip.Post(a, "/v1/guide/broken", func(_ context.Context, _ *stepRunIn) (*stepRunOut, error) {
		return nil, zip.ErrConflict("step 2 is blocked").WithBody(make(chan int))
	}, zip.WithFault[blockedBody](409))

	code, _, body := faultCall(t, a, "POST", "/v1/guide/broken", `{}`)
	if code != 409 {
		t.Fatalf("status = %d, want the refusal the handler chose", code)
	}
	if want := `{"status":409,"error":"step 2 is blocked"}`; strings.TrimSpace(body) != want {
		t.Fatalf("body = %s\nwant the envelope %s", body, want)
	}
}
