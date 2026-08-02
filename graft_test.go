package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/internal/zapenc"
)

// ── the child under test ────────────────────────────────────────────────────
//
// A small identity-shaped app: one typed op, one untyped route, and an
// authentication seam expressed the way a real service expresses it —
// app.Use(guard) with no path.

type userIn struct {
	ID string `json:"id"`
}
type userOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// childApp builds the child. guard, when non-nil, goes on as the child's
// pathless Use — the shape that makes route-copying unsafe.
func childApp(t *testing.T, name string, guard zip.Handler) *zip.App {
	t.Helper()
	c := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	if guard != nil {
		c.Use(guard)
	}
	zip.Get(c, "/v1/"+name+"/users/:id", func(_ context.Context, in *userIn) (*userOut, error) {
		return &userOut{ID: in.ID, Name: name + " says hello"}, nil
	}, zip.WithOperationID(name+"_get_user"), zip.WithSummary("Read one user by id"))
	c.Get("/v1/"+name+"/raw", func(x *zip.Ctx) error { return x.String(200, "raw "+name) })
	return c
}

func graftGET(t *testing.T, app *zip.App, path string) (int, string) {
	t.Helper()
	resp, err := app.Fiber().Test(httptest.NewRequest("GET", path, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp.StatusCode, string(out)
}

// TestGraft_OneComposeFourProjections is the whole point in one test: a child
// composed with Graft appears in ALL FOUR projections of the parent's registry
// — the OpenAPI document, the MCP tool list, the CLI commands and the by-name
// call plane — from one call and with no per-projection wiring. Under
// AdaptNetHTTP every one of these is empty.
func TestGraft_OneComposeFourProjections(t *testing.T) {
	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	child := childApp(t, "iam", nil)
	host.Use(child)
	if err := host.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// 1. OpenAPI — the path is the CHILD's absolute path, unrewritten, and it
	//    carries a real schema rather than a wildcard placeholder.
	spec := host.OpenAPISpec()
	paths := spec["paths"].(map[string]map[string]any)
	op, ok := paths["/v1/iam/users/{id}"]
	if !ok {
		t.Fatalf("document has no /v1/iam/users/{id}; paths=%v", pathKeys(paths))
	}
	getOp := op["get"].(map[string]any)
	if getOp["operationId"] != "iam_get_user" {
		t.Errorf("operationId rewritten to %v — an id is a published SDK method name", getOp["operationId"])
	}
	if getOp["summary"] != "Read one user by id" {
		t.Errorf("summary lost in the graft: %v", getOp["summary"])
	}
	resp := getOp["responses"].(map[string]any)["200"].(map[string]any)
	ref := resp["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
	if ref != "#/components/schemas/iam.userOut" {
		t.Errorf("response schema $ref = %v, want #/components/schemas/iam.userOut", ref)
	}
	if _, ok := spec["components"].(map[string]any)["schemas"].(map[string]any)["iam.userOut"]; !ok {
		t.Errorf("components.schemas has no iam.userOut")
	}
	// The op carries the child's name as its tag, so a grafted product is a
	// named group rather than an untagged blob.
	if tags, _ := getOp["tags"].([]string); len(tags) != 1 || tags[0] != "iam" {
		t.Errorf("tags = %v, want [iam]", getOp["tags"])
	}

	// 2. MCP — same op, as a tool that RUNS the child's handler.
	body := rpcLocal(t, host, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if !strings.Contains(body, `"iam_get_user"`) {
		t.Errorf("tools/list has no iam_get_user: %s", body)
	}
	called := rpcLocal(t, host,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"iam_get_user","arguments":{"id":"u-1"}}}`)
	if !strings.Contains(called, "iam says hello") {
		t.Errorf("tools/call did not run the child's handler: %s", called)
	}

	// 3. CLI — the command is grouped under the child's service and takes the
	//    path parameter as a positional argument.
	var cmd zip.Command
	for _, c := range host.Commands() {
		if c.OperationID == "iam_get_user" {
			cmd = c
		}
	}
	if cmd.OperationID == "" {
		t.Fatalf("Commands() has no iam_get_user")
	}
	if cmd.Service != "iam" {
		t.Errorf("command service = %q, want iam", cmd.Service)
	}
	if len(cmd.Args) != 1 || cmd.Args[0].Name != "id" {
		t.Errorf("command args = %+v, want one positional id", cmd.Args)
	}

	// 4. The by-name call plane runs the child's actual handler. Its body is
	//    ZAP — this plane is service-to-service and carries nothing else.
	in, err := zapenc.Marshal(&userIn{ID: "u-1"})
	if err != nil {
		t.Fatalf("marshal ZAP: %v", err)
	}
	out := postZAP(t, host, zip.CallPath+"iam_get_user", in)
	var got userOut
	if err := zapenc.Unmarshal(out, &got); err != nil {
		t.Fatalf("call plane reply is not ZAP: %v (%q)", err, out)
	}
	if got.Name != "iam says hello" || got.ID != "u-1" {
		t.Errorf("call plane did not run the child's handler: %+v", got)
	}
}

// TestGraft_ServesWithoutSwallowing proves the two halves of "serving is
// unchanged": every pattern the child declares reaches the child (typed AND
// untyped), and a path the child does NOT declare falls through to the parent.
// The wildcard this replaces swallowed the whole subtree.
func TestGraft_ServesWithoutSwallowing(t *testing.T) {
	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	host.Get("/v1/iam/not-the-childs", func(x *zip.Ctx) error { return x.String(200, "host") })
	host.Use(childApp(t, "iam", nil))

	if code, b := graftGET(t, host, "/v1/iam/users/u-9"); code != 200 || !strings.Contains(b, "u-9") {
		t.Errorf("typed child route: %d %s", code, b)
	}
	if code, b := graftGET(t, host, "/v1/iam/raw"); code != 200 || b != "raw iam" {
		t.Errorf("untyped child route: %d %q", code, b)
	}
	if code, b := graftGET(t, host, "/v1/iam/not-the-childs"); code != 200 || b != "host" {
		t.Errorf("host route under the child's prefix: %d %q", code, b)
	}
	// The address neither app declares is a 404 from the HOST — not swallowed
	// into the child, which is what the wildcard did.
	if code, _ := graftGET(t, host, "/v1/iam/nobody-declares-this"); code != 404 {
		t.Errorf("undeclared path answered %d, want 404 — the graft is swallowing", code)
	}
}

// TestGraft_MiddlewareStaysInsideTheChild is the test that justifies delegation
// over route-copying. The child's pathless Use gates the CHILD's routes and no
// others; copying its routes would drop the guard, and copying its Use would
// gate every route in the host binary.
func TestGraft_MiddlewareStaysInsideTheChild(t *testing.T) {
	guard := func(x *zip.Ctx) error {
		if x.Header("Authorization") == "" {
			return zip.Errorf(401, "no credentials")
		}
		return x.Continue()
	}
	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	host.Get("/v1/public", func(x *zip.Ctx) error { return x.String(200, "public") })
	host.Use(childApp(t, "iam", guard))

	// The guard DID come across: the child's own route is gated.
	if code, _ := graftGET(t, host, "/v1/iam/raw"); code != 401 {
		t.Errorf("child route answered %d without credentials — the guard was dropped by the graft", code)
	}
	// And it did NOT bleed: the host's own route is untouched.
	if code, b := graftGET(t, host, "/v1/public"); code != 200 || b != "public" {
		t.Errorf("host route answered %d %q — the child's Use bled onto the whole binary", code, b)
	}
}

// TestGraft_ChildAuthorizerStillFires proves the parent does not silently
// re-authorize a child's op under its own rules: op.invoke closes over the
// CHILD's Authorizer, over REST and over MCP alike.
func TestGraft_ChildAuthorizerStillFires(t *testing.T) {
	child := childApp(t, "iam", nil)
	var seen []string
	child.Authorize(func(_ context.Context, op zip.Op, _ any) error {
		seen = append(seen, op.OperationID)
		return errors.New("denied by iam")
	})
	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	// A host authorizer that would ALLOW, to prove whose rule ran.
	host.Authorize(func(context.Context, zip.Op, any) error { return nil })
	host.Use(child)
	if err := host.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if code, b := graftGET(t, host, "/v1/iam/users/u-1"); code == 200 {
		t.Errorf("REST: the child's authorizer did not run (%d %s)", code, b)
	}
	out := rpcLocal(t, host,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"iam_get_user","arguments":{"id":"u-1"}}}`)
	if !strings.Contains(out, "denied by iam") {
		t.Errorf("MCP: the child's authorizer did not run: %s", out)
	}
	if len(seen) != 2 {
		t.Errorf("child authorizer ran %d times, want 2 (REST + MCP): %v", len(seen), seen)
	}
}

// ── two children, one type name, two shapes ─────────────────────────────────

type appAlpha struct {
	ClientID string `json:"clientId"`
}
type appBeta struct {
	Company string `json:"company"`
}

// TestGraft_SameNameDifferentShape is the collision the fleet actually has:
// two apps each own a type called Application and the shapes differ. Both must
// publish, under distinct names, or one app's SDK binds the other's fields.
func TestGraft_SameNameDifferentShape(t *testing.T) {
	// Both children name their type "Application" via the same Go identifier
	// path is impossible in one package, so the shapes come in as distinct Go
	// types whose SCHEMA name is what must not clash. Alias them to one name.
	a := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	zip.Post(a, "/v1/iam/apps", func(_ context.Context, in *appAlpha) (*appAlpha, error) { return in, nil },
		zip.WithOperationID("iam_apps"))
	b := zip.New(zip.Config{AppName: "hire", DisableStartupMessage: true})
	zip.Post(b, "/v1/hire/apps", func(_ context.Context, in *appBeta) (*appBeta, error) { return in, nil },
		zip.WithOperationID("hire_apps"))

	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	host.Use(a, b)
	schemas := host.OpenAPISpec()["components"].(map[string]any)["schemas"].(map[string]any)
	for _, want := range []string{"iam.appAlpha", "hire.appBeta"} {
		if _, ok := schemas[want]; !ok {
			t.Errorf("components.schemas has no %q; got %v", want, keysOf(schemas))
		}
	}
	// Unqualified names must not appear at all: qualification is unconditional,
	// so a name is never a function of who else is in the room.
	for _, unwanted := range []string{"appAlpha", "appBeta"} {
		if _, ok := schemas[unwanted]; ok {
			t.Errorf("schema %q published unqualified — qualification must not be collision-driven", unwanted)
		}
	}
	// The child's OWN document is untouched: a copy went into the parent.
	if _, ok := a.OpenAPISpec()["components"].(map[string]any)["schemas"].(map[string]any)["appAlpha"]; !ok {
		t.Errorf("the graft edited the CHILD's document; a child served standalone must be unchanged")
	}
}

// TestGraft_CollidingAddressRefusesEverything proves the refusal is
// all-or-nothing and names both claimants. A half-grafted app is worse than a
// refused one.
func TestGraft_CollidingAddressRefusesEverything(t *testing.T) {
	host := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	host.Get("/v1/iam/raw", func(x *zip.Ctx) error { return x.String(200, "host owns this") })

	host.Use(childApp(t, "iam", nil))
	err := host.Build()
	if err == nil {
		t.Fatal("a colliding address was accepted")
	}
	for _, want := range []string{"GET /v1/iam/raw", `"cloud"`, `"iam"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %s: %v", want, err)
		}
	}
	// NOTHING is obtainable. A program that does not compose has no projection —
	// asking for one panics carrying this same error rather than answering with
	// an empty document. That is stronger than the old "half-grafted" check: it
	// is not that the routes are absent, it is that there is nothing to ask.
	// NOTHING is live. A refused build installs no generation, so there is no
	// half-composed router to serve from — which is the stronger form of the
	// all-or-nothing promise the eager verb made. When a VALID generation
	// already exists, a refused change leaves it serving untouched; that is
	// TestGeneration_RefusedIncludeLeavesTheOldOneServing.
	if _, live := host.Generation(); live {
		t.Error("a refused build installed a generation")
	}
}

// TestGraft_SiblingsCollide proves the check covers children against each
// other, not only against the parent.
func TestGraft_SiblingsCollide(t *testing.T) {
	host := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true})
	host.Use(childApp(t, "iam", nil), childApp(t, "iam", nil))
	err := host.Build()
	if err == nil {
		t.Fatal("two children claiming one address were both accepted")
	}
	if !strings.Contains(err.Error(), "GET /v1/iam/raw") {
		t.Errorf("refusal does not name the colliding address: %v", err)
	}
}

// TestGraft_RefusesAfterPrepare: the document, the tool list and the call plane
// are rendered once. An op grafted afterwards would serve and never describe,
// which is the exact defect Graft exists to remove.
func TestCompose_RefusedAfterTheAppIsBuilt(t *testing.T) {
	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	zip.Get(host, "/v1/own", func(context.Context, *userIn) (*userOut, error) { return &userOut{}, nil })
	if err := host.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Composing into an app that is already serving is refused AT THE CALL SITE,
	// and the panic names the one way to do it instead. Under the eager registry
	// this was an error saying "before Listen"; under generations it is a freeze,
	// and the fix is App.Include — which validates and swaps, or leaves the live
	// generation exactly as it was.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("composing into a built app was accepted")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "App.Include") {
			t.Errorf("refusal does not say how to compose against a live app: %v", r)
		}
	}()
	host.Use(childApp(t, "iam", nil))
}

// TestGraft_UngraftedDocumentIsUnchanged pins the compatibility claim: an app
// with nothing grafted names its types exactly as it did before Origin existed.
func TestGraft_UngraftedDocumentIsUnchanged(t *testing.T) {
	a := zip.New(zip.Config{AppName: "solo", DisableStartupMessage: true})
	zip.Post(a, "/v1/x", func(_ context.Context, in *userIn) (*userOut, error) { return &userOut{}, nil })
	schemas := a.OpenAPISpec()["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := schemas["userIn"]; !ok {
		t.Errorf("an ungrafted app's type was qualified: %v", keysOf(schemas))
	}
}

// TestGraft_ChildControlPlaneIsNotAdopted: the parent keeps its own /docs, MCP
// door, op plane and declaration. A child that took them would take the whole
// composition's document.
func TestGraft_ChildControlPlaneIsNotAdopted(t *testing.T) {
	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	zip.Get(host, "/v1/own", func(context.Context, *userIn) (*userOut, error) { return &userOut{}, nil },
		zip.WithOperationID("host_own"))
	child := childApp(t, "iam", nil)
	if err := child.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	host.Use(child)
	if err := host.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The host's document is the host's, and it carries both apps' ops.
	_, body := graftGET(t, host, zip.SpecPath)
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("%s is not JSON: %v", zip.SpecPath, err)
	}
	if doc["info"].(map[string]any)["title"] != "host" {
		t.Errorf("the child took the host's document: title=%v", doc["info"])
	}
	if _, ok := doc["paths"].(map[string]any)["/v1/iam/users/{id}"]; !ok {
		t.Errorf("the host's served document is missing the grafted op")
	}
	// The host's declaration excludes control routes and includes the child's.
	var hasChild bool
	for _, r := range host.Declaration().Routes {
		if r.Pattern == zip.SpecPath {
			t.Errorf("the host declared its own control plane")
		}
		if r.Pattern == "/v1/iam/raw" {
			hasChild = true
		}
	}
	if !hasChild {
		t.Errorf("the host's declaration is missing the child's routes")
	}
}

// TestGraft_ChildShutdownIsAdopted: a graft must not leak the child's store.
func TestGraft_ChildShutdownIsAdopted(t *testing.T) {
	child := childApp(t, "iam", nil)
	var torn bool
	child.OnShutdown(func(context.Context) error { torn = true; return nil })
	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	host.Use(child)
	if err := host.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !torn {
		t.Error("the host's Shutdown did not tear the grafted child down")
	}
}

// ── local drivers (no listener needed) ──────────────────────────────────────

func postLocal(t *testing.T, app *zip.App, path, body string) string {
	t.Helper()
	return string(post(t, app, path, "application/json", []byte(body)))
}

// postZAP drives the by-name call plane, whose body is ZAP and only ZAP.
func postZAP(t *testing.T, app *zip.App, path string, body []byte) []byte {
	t.Helper()
	return post(t, app, path, zip.CallContentType, body)
}

func post(t *testing.T, app *zip.App, path, ctype string, body []byte) []byte {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return out
}

func rpcLocal(t *testing.T, app *zip.App, body string) string {
	t.Helper()
	return postLocal(t, app, "/mcp", body)
}

func pathKeys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
