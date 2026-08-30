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

// A non-2xx used to be inexpressible. WithStatus panics on one — deliberately,
// because the status a SUCCESSFUL op answers with is one fact — and nothing else
// on the typed path could say a status either: the handler returns (*Out, error),
// the REST boundary writes a status and JSON, and the renderer writes a status and
// JSON. Nothing anywhere could set a HEADER, so a redirect was not merely
// undeclarable, it was unsendable: every OAuth leg that hands a browser to its
// provider stayed an untyped route, invisible to the document, the tool list, the
// CLI and the call plane.
//
// Now a non-2xx is declared as what it is — one more RESPONSE on the op — and a
// redirect is a value the handler returns.

const redirTarget = "https://accounts.example.com/o/oauth2/auth?client_id=abc%2F123&state=x+y&scope=a%20b"

type redirIn struct {
	Provider string `json:"provider"`
}

type redirOut struct {
	URL string `json:"url"`
}

// redirStart is the OAuth leg: its answer is a location, not a body.
func redirStart(_ context.Context, in *redirIn) (*redirOut, error) {
	return nil, &zip.Redirect{Location: redirTarget + "&provider=" + in.Provider}
}

// redirSeeOther names its own 3xx.
func redirSeeOther(_ context.Context, _ *redirIn) (*redirOut, error) {
	return nil, &zip.Redirect{Status: 303, Location: "/v1/redir/done"}
}

// redirBroken returns a redirect nothing can follow — a programming error, and
// the one case that must not reach the wire as a 3xx.
func redirBroken(_ context.Context, _ *redirIn) (*redirOut, error) {
	return nil, &zip.Redirect{Status: 302}
}

// redirBlocked refuses with a status its contract names.
func redirBlocked(_ context.Context, _ *redirIn) (*redirOut, error) {
	return nil, zip.ErrConflict("step 2 is blocked")
}

// redirVoid answers nothing at all — the nil-Out case, which must keep meaning
// 204 whatever else the op declares.
func redirVoid(_ context.Context, _ *redirIn) (*redirOut, error) { return nil, nil }

// redirMade answers a body, so its success status carries a schema.
func redirMade(_ context.Context, in *redirIn) (*redirOut, error) {
	return &redirOut{URL: in.Provider}, nil
}

// redirHop performs one request and returns what a browser sees: the status, the
// Location header, and the body.
func redirHop(t *testing.T, a *zip.App, method, path, body string) (int, string, string) {
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
	return resp.StatusCode, resp.Header.Get("Location"), string(raw)
}

// redirResponses is the whole responses object one op publishes.
func redirResponses(t *testing.T, a *zip.App, path, method string) map[string]any {
	t.Helper()
	paths, _ := a.OpenAPISpec()["paths"].(map[string]map[string]any)
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

func redirSchemas(t *testing.T, a *zip.App) map[string]any {
	t.Helper()
	comp, _ := a.OpenAPISpec()["components"].(map[string]any)
	defs, _ := comp["schemas"].(map[string]any)
	return defs
}

// entry reads one declared response.
func redirEntry(t *testing.T, resp map[string]any, code string) map[string]any {
	t.Helper()
	e, ok := resp[code].(map[string]any)
	if !ok {
		t.Fatalf("no %s response declared; got %v", code, resp)
	}
	return e
}

// THE FAILURE THIS REPLACES, and the rule it does NOT relax. WithStatus still
// refuses a non-2xx at declaration — that is what keeps the success status one
// fact in one place — and the non-2xx now has a vocabulary of its own.
func TestWithResponse_IsHowANon2xxIsExpressed(t *testing.T) {
	for _, code := range []int{302, 404, 500} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("WithStatus(%d) was accepted; the SUCCESS status must stay 2xx-only", code)
				}
			}()
			zip.WithStatus(code)
		}()
	}
	// The same codes, declared as what they are, do not panic.
	for _, code := range []int{301, 302, 303, 307, 308, 402, 404, 409, 500, 503} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("WithResponse(%d) panicked (%v); a non-2xx must be expressible", code, r)
				}
			}()
			zip.WithResponse(code)
		}()
	}
	// A 2xx is refused there for the mirror-image reason: WithStatus already
	// says it, and two options free to disagree is the split this closes.
	for _, code := range []int{200, 201, 204, 199, 600} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("WithResponse(%d) was accepted; it is not the success status", code)
				}
			}()
			zip.WithResponse(code)
		}()
	}
}

// A redirect reaches the WIRE: the status it chose, the Location header, and no
// body. Before this there was no seam that could set a header at all.
func TestRedirect_ReachesTheWire(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Get(a, "/v1/redir/oauth/:provider", redirStart, zip.WithResponse(302))

	code, loc, body := redirHop(t, a, "GET", "/v1/redir/oauth/google", "")
	if code != 302 {
		t.Fatalf("status = %d, want the 302 the handler returned", code)
	}
	// Byte for byte: a location is a URL the handler built, and re-encoding one
	// is how a signed callback loses its signature.
	if want := redirTarget + "&provider=google"; loc != want {
		t.Errorf("Location = %q, want %q", loc, want)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("body = %q, want none — a redirect's answer is the header", body)
	}
}

// The status is the one the handler named, not one zip picked.
func TestRedirect_StatusIsTheOneChosen(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Post(a, "/v1/redir/submit", redirSeeOther, zip.WithResponse(303))

	code, loc, _ := redirHop(t, a, "POST", "/v1/redir/submit", `{"provider":"x"}`)
	if code != 303 || loc != "/v1/redir/done" {
		t.Fatalf("status = %d Location = %q, want 303 /v1/redir/done", code, loc)
	}
}

// …and it reaches the DOCUMENT, which is the point: a status that only changed
// the wire would be a prettier side channel. A 3xx is described by its Location
// HEADER and declares no body, because that is its whole answer.
func TestRedirect_ReachesTheDocument(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Get(a, "/v1/redir/oauth/:provider", redirStart, zip.WithResponse(302))

	resp := redirResponses(t, a, "/v1/redir/oauth/{provider}", "get")
	got := redirEntry(t, resp, "302")
	if got["description"] != "found" {
		t.Errorf("description = %v, want the reason phrase", got["description"])
	}
	if _, ok := got["content"]; ok {
		t.Errorf("302 declares a body; a redirect answers with a header — %v", got)
	}
	headers, _ := got["headers"].(map[string]any)
	loc, ok := headers["Location"].(map[string]any)
	if !ok {
		t.Fatalf("302 declares no Location header; a client generated from this cannot follow it — %v", got)
	}
	if loc["required"] != true {
		t.Errorf("Location required = %v, want true", loc["required"])
	}
	if schema, _ := loc["schema"].(map[string]any); schema["type"] != "string" {
		t.Errorf("Location schema = %v, want a string", loc["schema"])
	}
	// The success answer is untouched — same status, same schema. A declared
	// redirect rides ALONGSIDE it; it does not replace what the op already said.
	success := redirEntry(t, resp, "200")
	if success["description"] != "ok" {
		t.Errorf("200 = %v, want the unchanged default", success)
	}
	content, _ := success["content"].(map[string]any)
	if _, ok := content["application/json"]; !ok {
		t.Errorf("200 lost its body; content = %v", content)
	}
}

// The nil-Out 204 default is intact — in the document above and on the WIRE
// here. Declaring a redirect says what an op CAN answer; it does not make it
// happen, and a handler that returns no Out still answers 204 (or the 2xx it
// declared).
func TestRedirect_NilOutDefaultIsIntact(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Post(a, "/v1/redir/void", redirVoid, zip.WithResponse(302))
	zip.Post(a, "/v1/redir/queued", redirVoid, zip.WithStatus(202), zip.WithResponse(302))

	if code, loc, body := redirHop(t, a, "POST", "/v1/redir/void", `{"provider":"x"}`); code != 204 ||
		loc != "" || strings.TrimSpace(body) != "" {
		t.Fatalf("status = %d Location = %q body = %q, want a bare 204", code, loc, body)
	}
	if code, _, _ := redirHop(t, a, "POST", "/v1/redir/queued", `{"provider":"x"}`); code != 202 {
		t.Fatalf("status = %d, want the declared 202 — WithStatus still owns the success status", code)
	}
	if e := redirEntry(t, redirResponses(t, a, "/v1/redir/queued", "post"), "202"); e["description"] != "accepted" {
		t.Errorf("202 = %v, want the declared success status in the document too", e)
	}
}

// A refusal the contract NAMES. The handler could always return one; what was
// missing is that the document said nothing about it, so every generated SDK
// read a deliberate 409 as an unexpected error. The declared shape is the
// envelope errorHandler actually writes — derived from the type that writes it,
// so it cannot drift from the wire.
func TestWithResponse_DeclaresARefusal(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Post(a, "/v1/redir/steps", redirBlocked, zip.WithResponse(409))

	resp := redirResponses(t, a, "/v1/redir/steps", "post")
	got := redirEntry(t, resp, "409")
	if got["description"] != "conflict" {
		t.Errorf("description = %v, want the reason phrase", got["description"])
	}
	content, _ := got["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, _ := media["schema"].(map[string]any)
	ref, _ := schema["$ref"].(string)
	if ref != "#/components/schemas/HTTPError" {
		t.Fatalf("409 schema = %v, want a $ref to the error envelope", schema)
	}
	def, _ := redirSchemas(t, a)["HTTPError"].(map[string]any)
	props, _ := def["properties"].(map[string]any)
	for _, field := range []string{"status", "error"} {
		if _, ok := props[field]; !ok {
			t.Errorf("the published envelope has no %q; properties = %v", field, props)
		}
	}
	// And the wire answers exactly that.
	code, _, body := redirHop(t, a, "POST", "/v1/redir/steps", `{"provider":"x"}`)
	if code != 409 || !strings.Contains(body, `"status":409`) || !strings.Contains(body, "step 2 is blocked") {
		t.Fatalf("status = %d body = %s, want the declared 409 envelope", code, body)
	}
}

// An op that declares nothing publishes exactly what it always did: this is
// additive, and a document nobody asked to change does not churn.
func TestWithResponse_DeclaringNothingChangesNothing(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Post(a, "/v1/redir/plain", redirMade)

	resp := redirResponses(t, a, "/v1/redir/plain", "post")
	if len(resp) != 1 {
		t.Fatalf("responses = %v, want only the success status", resp)
	}
	if e := redirEntry(t, resp, "200"); e["description"] != "ok" {
		t.Errorf("200 = %v, want the unchanged default", e)
	}
}

// A redirect nothing can follow is a server bug, and it is refused as one: a 3xx
// with no Location would be a status no client can act on and no document
// describes.
func TestRedirect_WithoutALocationIsRefused(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Get(a, "/v1/redir/broken", redirBroken, zip.WithResponse(302))

	code, loc, body := redirHop(t, a, "GET", "/v1/redir/broken", "")
	if code != 500 {
		t.Fatalf("status = %d, want 500 — an unfollowable redirect is a bug, not an answer", code)
	}
	if loc != "" {
		t.Errorf("Location = %q, want none", loc)
	}
	if !strings.Contains(body, "3xx status and a location") {
		t.Errorf("body = %s, want the reason", body)
	}
}

// A Location is the first handler-controlled value zip writes into a response
// HEADER, so the header-splitting question has to have an answer: it does, and it
// is one place — fasthttp strips CR and LF from every header value it writes
// (initHeaderValueBytes → removeNewLines). Pinned here because the guarantee this
// capability leans on lives in a dependency.
func TestRedirect_CannotSplitTheResponse(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Get(a, "/v1/redir/evil", func(_ context.Context, _ *redirIn) (*redirOut, error) {
		return nil, &zip.Redirect{Location: "/ok\r\nX-Evil: 1"}
	}, zip.WithResponse(302))

	req, err := http.NewRequest("GET", "/v1/redir/evil", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Evil"); got != "" {
		t.Fatalf("X-Evil = %q — a location injected a header", got)
	}
	if loc := resp.Header.Get("Location"); strings.ContainsAny(loc, "\r\n") {
		t.Fatalf("Location = %q, want the newlines gone", loc)
	}
}

// The op-call plane carries a status but no headers. It must still carry the
// redirect's OWN status: preserving what the op chose is the whole reason that
// plane has a status at all, and collapsing a 302 to a 500 would lose it on the
// one answer that is not a fault.
func TestRedirect_CrossesTheCallPlaneAsItsOwnStatus(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Get(a, "/v1/redir/oauth/:provider", redirStart,
		zip.WithOperationID("redir_start"), zip.WithResponse(302))
	a.Prepare()

	code, _, _ := redirHop(t, a, "POST", "/.well-known/zip/op/redir_start", "")
	if code != 302 {
		t.Fatalf("call plane status = %d, want the 302 the op chose", code)
	}
}

// MCP has neither a status nor a header, so the LOCATION is the answer — and it
// arrives as a result, not as isError: a model told its call failed would retry
// an OAuth start that worked.
func TestRedirect_MCPReportsTheLocation(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Get(a, "/v1/redir/oauth/:provider", redirStart,
		zip.WithOperationID("redir_start"), zip.WithResponse(302))
	a.Prepare()

	code, _, body := redirHop(t, a, "POST", "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"redir_start","arguments":{"provider":"google"}}}`)
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
	if res.Result.IsError {
		t.Errorf("tools/call reported isError for a redirect; a location is an answer — %s", body)
	}
	if len(res.Result.Content) == 0 {
		t.Fatalf("tools/call returned no content — %s", body)
	}
	var got struct {
		Status   int    `json:"status"`
		Location string `json:"location"`
	}
	if err := json.Unmarshal([]byte(res.Result.Content[0].Text), &got); err != nil {
		t.Fatalf("content is not the redirect as data (%v): %s", err, res.Result.Content[0].Text)
	}
	if got.Status != 302 || got.Location != redirTarget+"&provider=google" {
		t.Fatalf("mcp redirect = %+v, want the status and the location", got)
	}
}

// The CLI runs the same op in this process. A redirect is not a value to print,
// so it comes back as the value it is — with the location intact, rather than as
// nothing at all.
func TestRedirect_CLIReportsTheLocation(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Get(a, "/v1/redir/oauth/:provider", redirStart, zip.WithResponse(302))

	var out bytes.Buffer
	cli := a.CLI()
	cli.Out = &out

	err := cli.Run(context.Background(), []string{"redir", "oauth-get", "google"})
	var r *zip.Redirect
	if !errors.As(err, &r) {
		t.Fatalf("local run = %v, want the *zip.Redirect the handler returned", err)
	}
	if want := redirTarget + "&provider=google"; r.Location != want {
		t.Errorf("Location = %q, want %q", r.Location, want)
	}
	if !strings.Contains(err.Error(), "302 found: ") {
		t.Errorf("message = %q, want the status and the location", err.Error())
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("printed %q, want nothing — there is no result to print", out.String())
	}
}

// …and remotely, against a running service, it is the SAME value: read back off
// the status line and the Location header. Before this a redirecting op printed
// nothing and exited zero, which is the worst answer available.
func TestRedirect_RemoteCLIReportsTheLocation(t *testing.T) {
	a := zip.New(zip.Config{AppName: "rd", DisableStartupMessage: true})
	zip.Get(a, "/v1/redir/oauth/:provider", redirStart, zip.WithResponse(302))

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

	err = cli.Run(context.Background(), []string{"redir", "oauth-get", "google"})
	var r *zip.Redirect
	if !errors.As(err, &r) {
		t.Fatalf("remote run = %v, want a *zip.Redirect", err)
	}
	if r.Status != 302 || r.Location != redirTarget+"&provider=google" {
		t.Fatalf("remote redirect = %+v, want the wire's status and location", r)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("printed %q, want nothing", out.String())
	}
}
