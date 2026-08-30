package zip_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A typed op decodes its body before the handler runs, which is what lets one
// handler answer four codecs — and what made three kinds of route unwritable as
// ops at all:
//
//   - a webhook that verifies a signature OVER THE BYTES (Slack v0 HMAC,
//     GitHub's X-Hub-Signature-256, Discord's Ed25519). A struct decoded from a
//     payload is not the payload, and a re-encoding of it signs to something
//     else, so verification against a decoded value verifies nothing;
//   - an upload whose body is a PDF, with no In to decode into;
//   - a route that must answer 200 to a body it cannot parse, because a sender
//     that retries every 4xx turns one malformed message into a storm.
//
// WithRawBody declares the body OPAQUE and BodyOf hands the handler the bytes
// this projection carried. These tests pin the capability and the failure it
// replaces, on every surface the op is projected onto.

const (
	rawSigHeader = "X-Hub-Signature-256"
	rawSecret    = "s3cr3t"
)

// rawSign is the signature a sender computes over the payload it sends.
func rawSign(body []byte) string {
	m := hmac.New(sha256.New, []byte(rawSecret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// rawSigKey is where the app's middleware puts the one header this route needs.
// A typed handler declares its inputs rather than reading the request, so what a
// route needs BESIDE its In arrives the way anything else per-request does — on
// the context. The BYTES are what only zip can supply, and that is what BodyOf
// is for.
type rawSigKey struct{}

type rawHookIn struct {
	Provider string `json:"provider"` // the segment the router matched
	Ref      string `json:"ref"`      // a query value beside it
}

type rawHookOut struct {
	Provider string `json:"provider"`
	Ref      string `json:"ref"`
	Verified bool   `json:"verified"`
	SHA      string `json:"sha"`
	Bytes    int    `json:"bytes"`
	Kind     string `json:"kind"`
}

// rawHook is the shape every one of those webhooks has: verify over the bytes,
// and only then read what they say.
func rawHook(ctx context.Context, in *rawHookIn) (*rawHookOut, error) {
	body := zip.BodyOf(ctx)
	sig, _ := ctx.Value(rawSigKey{}).(string)
	out := &rawHookOut{
		Provider: in.Provider,
		Ref:      in.Ref,
		Bytes:    len(body),
		SHA:      fmt.Sprintf("%x", sha256.Sum256(body)),
		Verified: hmac.Equal([]byte(sig), []byte(rawSign(body))),
	}
	var payload struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(body, &payload) // a body that does not parse is not an error here
	out.Kind = payload.Kind
	return out, nil
}

func rawHookApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "hooks", DisableStartupMessage: true})
	a.Use(func(c *zip.Ctx) error {
		c.SetContext(context.WithValue(c.Context(), rawSigKey{}, c.Header(rawSigHeader)))
		return c.Continue()
	})
	zip.Post(a, "/v1/hooks/:provider", rawHook,
		zip.WithRawBody("application/json"), zip.WithOperationID("hooks_receive"))
	return a
}

// rawPost sends bytes, under a media type, with an optional signature.
func rawPost(t *testing.T, a *zip.App, target, media string, body []byte, sig string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", media)
	if sig != "" {
		req.Header.Set(rawSigHeader, sig)
	}
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for {
		n, rerr := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, string(out)
}

// The bytes that arrived are the bytes the handler sees — byte for byte, so a
// signature computed over the payload verifies against them. The same test pins
// WHY a decoded In cannot stand in: the canonical encoding of that very value
// signs to something else.
func TestRawBody_TheBytesThatArrivedAreTheBytesTheHandlerSees(t *testing.T) {
	a := rawHookApp(t)

	// Deliberately NOT the canonical encoding of its own value: spaced out, with
	// keys in an order json.Marshal would never choose. This is what a sender
	// signs.
	payload := []byte("{\"kind\":  \"push\",\n  \"id\": 7}")
	code, body := rawPost(t, a, "/v1/hooks/github?ref=main", "application/json", payload, rawSign(payload))
	if code != 200 {
		t.Fatalf("POST = %d %s, want 200", code, body)
	}
	var got rawHookOut
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(payload)); got.SHA != want {
		t.Errorf("the handler saw different bytes: sha %s, want %s", got.SHA, want)
	}
	if got.Bytes != len(payload) {
		t.Errorf("the handler saw %d bytes, want %d", got.Bytes, len(payload))
	}
	if !got.Verified {
		t.Errorf("the signature over the received bytes did not verify: %s", body)
	}
	if got.Kind != "push" {
		t.Errorf("kind = %q, want push — the handler parses the bytes itself", got.Kind)
	}
	if got.Provider != "github" || got.Ref != "main" {
		t.Errorf("URL did not bind: provider %q ref %q", got.Provider, got.Ref)
	}

	// The failure this replaces, stated as arithmetic: a re-encoding of the
	// decoded value is a different message with the same meaning, so an op that
	// only ever held the decoded value could not have verified anything.
	reencoded, err := json.Marshal(map[string]any{"kind": "push", "id": 7})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(reencoded, payload) {
		t.Fatalf("fixture is too tame: %s re-encodes to itself", payload)
	}
	if rawSign(reencoded) == rawSign(payload) {
		t.Fatalf("re-encoding signed the same; the fixture proves nothing")
	}
}

// A body that does not parse used to be a 400 the sender retried. It now reaches
// the handler, which answers for it.
func TestRawBody_AnUnparseableBodyReachesTheHandlerInsteadOf400(t *testing.T) {
	junk := []byte("\x00\x01not json at all")

	// BEFORE — the same route, typed the only way it could be: invoke decodes
	// first, so the handler never runs and the answer is a 4xx every webhook
	// platform retries.
	typed := zip.New(zip.Config{AppName: "hooks", DisableStartupMessage: true})
	zip.Post(typed, "/v1/hooks/:provider", rawHook)
	code, body := rawPost(t, typed, "/v1/hooks/github", "application/json", junk, "")
	if code != 400 || !strings.Contains(body, "invalid body") {
		t.Fatalf("a decoded op should refuse it: %d %s, want 400 invalid body", code, body)
	}

	// AFTER — declared opaque, the bytes arrive intact and the handler decides.
	raw := rawHookApp(t)
	code, body = rawPost(t, raw, "/v1/hooks/github", "application/json", junk, rawSign(junk))
	if code != 200 {
		t.Fatalf("POST = %d %s, want 200", code, body)
	}
	var got rawHookOut
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(junk)); got.SHA != want {
		t.Errorf("sha %s, want %s — unparseable bytes must still arrive whole", got.SHA, want)
	}
	if !got.Verified {
		t.Errorf("a signature over unparseable bytes must still verify: %s", body)
	}
	if got.Kind != "" {
		t.Errorf("kind = %q; nothing parsed out of junk", got.Kind)
	}
}

type rawAckOut struct {
	OK bool `json:"ok"`
}

// The retry-storm route in full: answer the status the op DECLARED, with no
// body, to a payload nothing can read.
func TestRawBody_AnswersTheDeclaredStatusToABodyNothingCanParse(t *testing.T) {
	a := zip.New(zip.Config{AppName: "hooks", DisableStartupMessage: true})
	var seen []byte
	zip.Post(a, "/v1/hooks/ack", func(ctx context.Context, _ *rawHookIn) (*rawAckOut, error) {
		seen = append([]byte(nil), zip.BodyOf(ctx)...)
		return nil, nil // nothing to say; the status is the whole answer
	}, zip.WithRawBody(), zip.WithStatus(200))

	junk := []byte{0x1f, 0x8b, 0x08, 0x00, 0xff}
	code, body := rawPost(t, a, "/v1/hooks/ack", "application/octet-stream", junk, "")
	if code != 200 {
		t.Fatalf("POST = %d %s, want the declared 200", code, body)
	}
	if body != "" {
		t.Errorf("body = %q, want empty — a void op says nothing", body)
	}
	if !bytes.Equal(seen, junk) {
		t.Errorf("handler saw %v, want %v", seen, junk)
	}
}

type rawDeckIn struct {
	Company  string `json:"company"`
	Filename string `json:"filename" validate:"required"`
}

type rawDeckOut struct {
	Company  string `json:"company"`
	Filename string `json:"filename"`
	Bytes    int    `json:"bytes"`
	PDF      bool   `json:"pdf"`
}

func rawDeck(ctx context.Context, in *rawDeckIn) (*rawDeckOut, error) {
	body := zip.BodyOf(ctx)
	return &rawDeckOut{
		Company:  in.Company,
		Filename: in.Filename,
		Bytes:    len(body),
		PDF:      bytes.HasPrefix(body, []byte("%PDF-")),
	}, nil
}

const rawDeckPath = "/v1/company/:company/fundraise/deck"

func rawDeckApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "company", DisableStartupMessage: true})
	zip.Post(a, rawDeckPath, rawDeck,
		zip.WithRawBody("application/pdf"), zip.WithOperationID("company_deck_upload"))
	return a
}

// The URL still addresses the request. An opaque body takes the body away and
// nothing else: path and query bind onto In, `validate:` runs on it, and the
// authorizer sees the value the handler will act on.
func TestRawBody_TheURLStillBindsValidatesAndAuthorizes(t *testing.T) {
	a := rawDeckApp(t)
	var authorized zip.Op
	var authorizedIn *rawDeckIn
	a.Authorize(func(_ context.Context, op zip.Op, in any) error {
		authorized = op
		authorizedIn, _ = in.(*rawDeckIn)
		return nil
	})

	pdf := append([]byte("%PDF-1.7\n"), 0x00, 0x01, 0x02)
	code, body := rawPost(t, a, "/v1/company/acme/fundraise/deck?filename=seed.pdf", "application/pdf", pdf, "")
	if code != 200 {
		t.Fatalf("POST = %d %s, want 200", code, body)
	}
	var got rawDeckOut
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if got.Company != "acme" || got.Filename != "seed.pdf" {
		t.Errorf("URL did not bind: %+v", got)
	}
	if !got.PDF || got.Bytes != len(pdf) {
		t.Errorf("the PDF did not arrive whole: %+v", got)
	}
	if authorizedIn == nil || authorizedIn.Company != "acme" {
		t.Errorf("the authorizer did not see the bound input: %+v", authorizedIn)
	}
	if authorized.OperationID != "company_deck_upload" {
		t.Errorf("the authorizer saw op %+v", authorized)
	}

	// And a required URL value is still required: the body being opaque says
	// nothing about the rest of the input.
	code, body = rawPost(t, a, "/v1/company/acme/fundraise/deck", "application/pdf", pdf, "")
	if code != 400 || !strings.Contains(body, "filename") {
		t.Fatalf("missing required filename = %d %s, want 400 naming filename", code, body)
	}
}

// An op that declared nothing about its body has none of these bytes: its In was
// decoded from them, and a second way to read one input is a second thing to
// keep true.
func TestRawBody_BodyOfIsNilWithoutTheDeclaration(t *testing.T) {
	a := zip.New(zip.Config{AppName: "hooks", DisableStartupMessage: true})
	zip.Post(a, "/v1/hooks/plain", func(ctx context.Context, in *rawHookIn) (*rawHookOut, error) {
		return &rawHookOut{Provider: in.Provider, Bytes: len(zip.BodyOf(ctx))}, nil
	})
	code, body := rawPost(t, a, "/v1/hooks/plain", "application/json", []byte(`{"provider":"github"}`), "")
	if code != 200 {
		t.Fatalf("POST = %d %s, want 200", code, body)
	}
	if !strings.Contains(body, `"provider":"github"`) {
		t.Errorf("the body should still have decoded into In: %s", body)
	}
	if !strings.Contains(body, `"bytes":0`) {
		t.Errorf("BodyOf must be empty for an op that decodes: %s", body)
	}
}

// A body to read is the one thing the option cannot supply for itself, so a
// method that carries none is refused at registration rather than handing a
// signature check nothing at all.
func TestRawBody_RefusedOnAMethodThatCarriesNoBody(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(*zip.App)
	}{
		{"DELETE", func(a *zip.App) { zip.Delete(a, "/v1/hooks/:provider", rawHook, zip.WithRawBody()) }},
		{"GET", func(a *zip.App) { zip.Get(a, "/v1/hooks/:provider", rawHook, zip.WithRawBody()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s with a raw body should not register", tc.name)
				}
				if !strings.Contains(fmt.Sprint(r), "no body to read") {
					t.Fatalf("panic = %v, want it to say there is no body to read", r)
				}
			}()
			tc.register(zip.New(zip.Config{AppName: "hooks", DisableStartupMessage: true}))
		})
	}
}

// ---------------------------------------------------------------------------
// The document — and through it every generated SDK
// ---------------------------------------------------------------------------

// rawSpecOp is one operation of the document, read back through JSON so the
// assertions are made on what a generator actually receives.
func rawSpecOp(t *testing.T, a *zip.App, method, path string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(a.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	ops, ok := doc.Paths[path]
	if !ok {
		t.Fatalf("no path %s in %s", path, raw)
	}
	body, ok := ops[strings.ToLower(method)]
	if !ok {
		t.Fatalf("no %s on %s in %s", method, path, raw)
	}
	var op map[string]any
	if err := json.Unmarshal(body, &op); err != nil {
		t.Fatalf("unmarshal op: %v", err)
	}
	// The In type must not be published as a body schema: it is not the body.
	if _, defined := doc.Components.Schemas["rawDeckIn"]; defined {
		t.Errorf("rawDeckIn is in components.schemas; an opaque body has no In schema:\n%s", raw)
	}
	return op
}

// The document publishes the media an opaque body carries and no schema for it,
// which is what a generator reads as bytes — where before there was no way to
// say it, so such a route could only be an untyped handler, invisible to the
// document, the SDK, the tool list and the command line alike.
func TestRawBody_OpenAPIPublishesTheMediaAndNoSchema(t *testing.T) {
	op := rawSpecOp(t, rawDeckApp(t), "POST", "/v1/company/{company}/fundraise/deck")

	rb, ok := op["requestBody"].(map[string]any)
	if !ok {
		t.Fatalf("no requestBody: %v", op)
	}
	if rb["required"] != true {
		t.Errorf("requestBody.required = %v, want true", rb["required"])
	}
	content, _ := rb["content"].(map[string]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want exactly the declared media", content)
	}
	media, ok := content["application/pdf"].(map[string]any)
	if !ok {
		t.Fatalf("content is not keyed on the declared media: %v", content)
	}
	schema, _ := media["schema"].(map[string]any)
	if schema["type"] != "string" || schema["format"] != "binary" {
		t.Errorf("schema = %v, want a binary string", schema)
	}
	if _, json := content["application/json"]; json {
		t.Errorf("an opaque body must not be published as JSON: %v", content)
	}

	// The URL is the whole of a raw op's structured input, so its parameters are
	// what the document has to declare — the path segment AND the query value.
	params, _ := op["parameters"].([]any)
	kinds := map[string]string{}
	for _, p := range params {
		d, _ := p.(map[string]any)
		kinds[fmt.Sprint(d["name"])] = fmt.Sprint(d["in"])
	}
	if kinds["company"] != "path" {
		t.Errorf("company should be a path parameter: %v", params)
	}
	if kinds["filename"] != "query" {
		t.Errorf("filename should be a query parameter — a raw op binds its input from the URL: %v", params)
	}

	// The response is unchanged: an opaque body says nothing about the answer.
	resp, _ := op["responses"].(map[string]any)
	if _, ok := resp["200"]; !ok {
		t.Errorf("responses = %v, want 200", resp)
	}
}

// Named nothing, an opaque body is octet-stream — the media type HTTP already
// means "bytes" — and several media are published as several, for a sender that
// picks (GitHub sends either JSON or form encoding).
func TestRawBody_MediaDefaultsToOctetStreamAndCarriesEveryOneDeclared(t *testing.T) {
	a := zip.New(zip.Config{AppName: "hooks", DisableStartupMessage: true})
	zip.Post(a, "/v1/hooks/bytes", rawHook, zip.WithRawBody())
	zip.Post(a, "/v1/hooks/either", rawHook,
		zip.WithRawBody("application/json", "application/x-www-form-urlencoded"))

	one := rawSpecOp(t, a, "POST", "/v1/hooks/bytes")
	content, _ := one["requestBody"].(map[string]any)["content"].(map[string]any)
	if _, ok := content["application/octet-stream"]; !ok || len(content) != 1 {
		t.Errorf("content = %v, want just application/octet-stream", content)
	}

	both := rawSpecOp(t, a, "POST", "/v1/hooks/either")
	content, _ = both["requestBody"].(map[string]any)["content"].(map[string]any)
	for _, want := range []string{"application/json", "application/x-www-form-urlencoded"} {
		media, ok := content[want].(map[string]any)
		if !ok {
			t.Fatalf("content = %v, missing %s", content, want)
		}
		schema, _ := media["schema"].(map[string]any)
		if schema["format"] != "binary" {
			t.Errorf("%s schema = %v, want binary — the media is JSON, the BODY is bytes", want, schema)
		}
	}
}

// ---------------------------------------------------------------------------
// The two by-name planes
// ---------------------------------------------------------------------------

// An MCP tool's arguments are a JSON object and the op-call plane's body is a ZAP
// message; neither is "the bytes a client sent". So a raw op is not on either
// surface — unlisted AND uncallable, from one predicate, rather than advertised
// as a tool no model can call correctly.
func TestRawBody_IsNotOnTheByNamePlanes(t *testing.T) {
	a := rawHookApp(t)
	zip.Post(a, "/v1/hooks/plain", func(_ context.Context, in *rawHookIn) (*rawHookOut, error) {
		return &rawHookOut{Provider: in.Provider}, nil
	}, zip.WithOperationID("hooks_plain"))
	a.Prepare()

	names := map[string]bool{}
	for _, tool := range a.MCPTools() {
		names[fmt.Sprint(tool["name"])] = true
	}
	if names["hooks_receive"] {
		t.Errorf("the raw op is listed as an MCP tool: %v", names)
	}
	if !names["hooks_plain"] {
		t.Errorf("its ordinary sibling must still be a tool: %v", names)
	}

	// tools/call agrees with tools/list, because both read the same lookup.
	code, body := rawPost(t, a, "/mcp", "application/json",
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hooks_receive","arguments":{}}}`), "")
	if code != 200 || !strings.Contains(body, "unknown tool") {
		t.Errorf("tools/call on a raw op = %d %s, want an unknown-tool error", code, body)
	}

	// The op-call plane, same predicate: not found by name.
	code, body = rawPost(t, a, zip.CallPath+"hooks_receive", zip.CallContentType, []byte{}, "")
	if code != 404 {
		t.Errorf("call plane on a raw op = %d %s, want 404", code, body)
	}
	if code, _ = rawPost(t, a, zip.CallPath+"hooks_plain", zip.CallContentType, []byte{}, ""); code == 404 {
		t.Errorf("its ordinary sibling must still be callable by name, got 404")
	}
}

// A tools/call carries the caller's identity like every other invoke: the org a
// gateway asserted is the request's, not one projection's. It read back empty
// over MCP while REST saw it, so one op decided two ways about one caller —
// which is also what any ctx-borne read (BodyOf included) depends on.
func TestRawBody_MCPCallCarriesTheCallerIdentity(t *testing.T) {
	a := zip.New(zip.Config{AppName: "hooks", DisableStartupMessage: true})
	zip.Post(a, "/v1/hooks/who", func(ctx context.Context, _ *rawHookIn) (*rawHookOut, error) {
		return &rawHookOut{Provider: zip.CallerOf(ctx).Org}, nil
	}, zip.WithOperationID("hooks_who"))
	a.Prepare()

	req, err := http.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hooks_who","arguments":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(zip.HeaderOrg, "acme")
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(out), `acme`) {
		t.Errorf("tools/call lost the caller's org: %v", out)
	}
}

// ---------------------------------------------------------------------------
// The command line
// ---------------------------------------------------------------------------

// A command cannot spell bytes as a flag, so it names the FILE that holds them
// and sends it verbatim. Its other flags still ride the URL, which is where the
// route reads them from.
func TestRawBody_CLISendsTheFileVerbatim(t *testing.T) {
	a := rawDeckApp(t)
	cmds := a.Commands()
	var deck zip.Command
	for _, c := range cmds {
		if c.OperationID == "company_deck_upload" {
			deck = c
		}
	}
	if deck.OperationID == "" {
		t.Fatalf("the raw op is not a command: %v", cmds)
	}
	if deck.BodyMedia != "application/pdf" {
		t.Errorf("BodyMedia = %q, want the declared media", deck.BodyMedia)
	}
	flags := map[string]string{}
	for _, f := range deck.Flags {
		flags[f.Name] = f.Type
	}
	if flags["body"] != "file" {
		t.Errorf("flags = %v, want --body to name a file", deck.Flags)
	}
	if flags["filename"] != "string" {
		t.Errorf("flags = %v, want the URL-borne --filename beside it", deck.Flags)
	}
	if len(flags) != 2 {
		t.Errorf("flags = %v; an opaque body has no fields to spell flags from", deck.Flags)
	}

	pdf := append([]byte("%PDF-1.7\n"), 0x7f, 0x00, 0xfe)
	file := filepath.Join(t.TempDir(), "deck.pdf")
	if err := os.WriteFile(file, pdf, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cli := &zip.CLI{Name: "co", Commands: cmds, Out: &out} // LocalInvoke by default
	if err := cli.Run(context.Background(), []string{
		"company", "deck-upload", "acme", "--filename", "seed.pdf", "--body", file,
	}); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	var got rawDeckOut
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", out.String(), err)
	}
	if got.Bytes != len(pdf) || !got.PDF {
		t.Errorf("the file did not arrive whole: %+v", got)
	}
	if got.Company != "acme" || got.Filename != "seed.pdf" {
		t.Errorf("the URL-borne values did not bind: %+v", got)
	}

	// The file is the body, so it is required, and a missing one is refused
	// before anything is sent.
	out.Reset()
	err := cli.Run(context.Background(), []string{"company", "deck-upload", "acme", "--filename", "seed.pdf"})
	if err == nil || !strings.Contains(err.Error(), "--body is required") {
		t.Errorf("without --body: %v, want it required", err)
	}
	out.Reset()
	err = cli.Run(context.Background(), []string{
		"company", "deck-upload", "acme", "--filename", "s.pdf", "--body", file + ".missing",
	})
	if err == nil || !strings.Contains(err.Error(), "--body") {
		t.Errorf("with an unreadable --body: %v, want the flag named", err)
	}
}

// The two derivations are one derivation: the command read off the registry and
// the command read off the document that registry generates are the same
// command, opaque body and all. Which is what makes a raw route reachable from a
// CLI that links nothing of the service — the document carries the media, so the
// client sends the file under it.
func TestRawBody_SpecAndRegistryAgree(t *testing.T) {
	a := rawDeckApp(t)
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
		if r.BodyMedia != s.BodyMedia {
			t.Errorf("%s %s media: registry %q, spec %q", r.Service, r.Name, r.BodyMedia, s.BodyMedia)
		}
		if r.NoBody != s.NoBody {
			t.Errorf("%s %s NoBody: registry %v, spec %v — an opaque body is not an absent one",
				r.Service, r.Name, r.NoBody, s.NoBody)
		}
		if fmt.Sprint(r.Flags) != fmt.Sprint(s.Flags) {
			t.Errorf("%s %s flags: registry %v, spec %v", r.Service, r.Name, r.Flags, s.Flags)
		}
		if fmt.Sprint(r.Args) != fmt.Sprint(s.Args) {
			t.Errorf("%s %s args: registry %v, spec %v", r.Service, r.Name, r.Args, s.Args)
		}
	}
}

// And over the wire: the remote invoker sends the file as the body under the
// media the op declared — a PDF announced as JSON is a request an edge is
// entitled to refuse — with the URL-borne flags in the query string the route
// reads them from.
func TestRawBody_RemoteInvokeSendsTheDeclaredMedia(t *testing.T) {
	// Built here rather than through rawDeckApp: what the WIRE carried is the
	// claim, and only a middleware registered before the route sees it.
	a := zip.New(zip.Config{AppName: "company", DisableStartupMessage: true})
	var media, query string
	a.Use(func(c *zip.Ctx) error {
		media, query = c.Header("Content-Type"), c.Query("filename")
		return c.Continue()
	})
	zip.Post(a, rawDeckPath, rawDeck,
		zip.WithRawBody("application/pdf"), zip.WithOperationID("company_deck_upload"))

	addr := freeAddr(t)
	go func() { _ = a.Listen("http://" + addr) }()
	defer func() { _ = a.Shutdown() }()
	waitHTTP(t, "http://"+addr+"/.well-known/openapi.json")

	pdf := append([]byte("%PDF-1.7\n"), 0x00, 0xff)
	file := filepath.Join(t.TempDir(), "deck.pdf")
	if err := os.WriteFile(file, pdf, 0o600); err != nil {
		t.Fatal(err)
	}

	remote := zip.Remote{Base: "http://" + addr}
	spec, err := remote.Spec(context.Background())
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	cmds, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	var out bytes.Buffer
	cli := &zip.CLI{Name: "co", Commands: cmds, Invoke: remote.Invoke, Out: &out}
	if err := cli.Run(context.Background(), []string{
		"company", "deck-upload", "acme", "--filename", "seed.pdf", "--body", file,
	}); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if media != "application/pdf" {
		t.Errorf("Content-Type = %q, want the op's declared media", media)
	}
	if query != "seed.pdf" {
		t.Errorf("filename reached the route as %q, want seed.pdf in the query string", query)
	}
	var got rawDeckOut
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", out.String(), err)
	}
	if got.Bytes != len(pdf) || !got.PDF || got.Filename != "seed.pdf" || got.Company != "acme" {
		t.Errorf("what the service received: %+v", got)
	}
}
