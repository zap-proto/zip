package zip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The claim under test: a GraphQL field reaches an op through the SAME contract
// the REST route uses, and no further. Every check an op makes it makes here;
// nothing an op withholds becomes reachable because a second door was opened.

type resUser struct {
	ID    string  `json:"id"`
	Email string  `json:"email"`
	Boss  *resHum `json:"boss"`
}

type resHum struct {
	Name string `json:"name"`
	Rank int    `json:"rank"`
}

type resGet struct {
	ID string `json:"id" validate:"required"`
}

type resMake struct {
	Email string `json:"email" validate:"required"`
}

// resAmb declares a header. Over REST its value comes from the request; the
// point of the tests below is that it can come from nowhere else.
type resAmb struct {
	Org string `json:"org" header:"X-Org-Id"`
	Q   string `json:"q"`
}

type resWho struct {
	Org string `json:"org"`
}

func resApp(t *testing.T) *App {
	t.Helper()
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	app.Get("/v1/users/:id", func(ctx context.Context, in *resGet) (*resUser, error) {
		if in.ID == "gone" {
			return nil, errors.New("no such user")
		}
		return &resUser{ID: in.ID, Email: in.ID + "@example.com",
			Boss: &resHum{Name: "ada", Rank: 1}}, nil
	}, WithOperationID("user"), WithSummary("One user by id"))
	app.Post("/v1/users", func(ctx context.Context, in *resMake) (*resUser, error) {
		return &resUser{ID: "new", Email: in.Email}, nil
	}, WithOperationID("createUser"))
	app.Get("/v1/ambient", func(ctx context.Context, in *resAmb) (*resWho, error) {
		return &resWho{Org: in.Org}, nil
	}, WithOperationID("ambient"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

type resReply struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message string `json:"message"`
		Path    []any  `json:"path"`
	} `json:"errors"`
	code int
}

// messages joins every error the reply carries, for assertions that care that a
// reason was GIVEN rather than exactly how it was worded.
func (r resReply) messages() string {
	var b []string
	for _, e := range r.Errors {
		b = append(b, e.Message)
	}
	return strings.Join(b, " | ")
}

func ask(t *testing.T, app *App, query string, vars map[string]any, header map[string]string) resReply {
	t.Helper()
	body, err := json.Marshal(GraphRequest{Query: query, Variables: vars})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, GraphPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", GraphPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out resReply
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("reply is not JSON (%s): %v", raw, err)
	}
	out.code = resp.StatusCode
	return out
}

// The base case: a query runs the op and answers with the fields asked for.
func TestAQueryRunsTheOpAndAnswers(t *testing.T) {
	r := ask(t, resApp(t), `{ user(id: "u1") { id email } }`, nil, nil)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	got, _ := r.Data["user"].(map[string]any)
	if got["id"] != "u1" || got["email"] != "u1@example.com" {
		t.Fatalf("the op did not run, or its result did not survive: %#v", r.Data)
	}
}

// A selection is a FILTER. A field the caller did not ask for must not come
// back — answering with more than was requested is how a projection leaks.
func TestOnlyTheSelectedFieldsComeBack(t *testing.T) {
	r := ask(t, resApp(t), `{ user(id: "u1") { id } }`, nil, nil)
	got, _ := r.Data["user"].(map[string]any)
	if _, leaked := got["email"]; leaked {
		t.Fatalf("a field nobody asked for was returned: %#v", got)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly the one selected field, got %#v", got)
	}
}

func TestNestedSelectionsProject(t *testing.T) {
	r := ask(t, resApp(t), `{ user(id: "u1") { boss { name } } }`, nil, nil)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	boss, _ := r.Data["user"].(map[string]any)["boss"].(map[string]any)
	if boss["name"] != "ada" {
		t.Fatalf("nested field lost: %#v", r.Data)
	}
	if _, leaked := boss["rank"]; leaked {
		t.Fatalf("an unselected nested field came back: %#v", boss)
	}
}

// ★ THE SECURITY PROPERTY. A mutating op must not be invocable by a query
// operation. Publishing the split in the schema is documentation; this is the
// part that enforces it.
func TestAMutationIsNotReachableFromAQuery(t *testing.T) {
	r := ask(t, resApp(t), `query { createUser(email: "x@y.z") { id } }`, nil, nil)
	if len(r.Errors) == 0 {
		t.Fatalf("a POST op ran inside a query operation — a write reached through a read: %#v", r.Data)
	}
	if got := r.Data["createUser"]; got != nil {
		t.Fatalf("the write produced data inside a query: %#v", got)
	}
	if !strings.Contains(r.messages(), "createUser") {
		t.Errorf("the refusal does not name the field: %s", r.messages())
	}
	// The same op through a mutation operation works, so the refusal above is
	// the SPLIT and not a broken registration.
	ok := ask(t, resApp(t), `mutation { createUser(email: "x@y.z") { id email } }`, nil, nil)
	if len(ok.Errors) != 0 {
		t.Fatalf("the mutation itself is broken: %s", ok.messages())
	}
	if ok.Data["createUser"].(map[string]any)["email"] != "x@y.z" {
		t.Fatalf("mutation did not run: %#v", ok.Data)
	}
}

// ★ THE OTHER SECURITY PROPERTY. A `header:` field is ambient — its value comes
// from the request, set by whatever runs in front of the handler. A client that
// could pass it as an argument could forge what the gateway asserts.
func TestAHeaderCannotBeSuppliedAsAnArgument(t *testing.T) {
	app := resApp(t)

	if s := app.GraphQLSDL(); strings.Contains(section(s, "type Query {"), "org:") {
		t.Errorf("the schema publishes an ambient header as an argument:\n%s", section(s, "type Query {"))
	}

	forged := ask(t, app, `{ ambient(org: "evil") { org } }`, nil, map[string]string{"X-Org-Id": "real"})
	if len(forged.Errors) == 0 {
		t.Fatalf("an argument set a header-bound field: %#v", forged.Data)
	}
	if got := forged.Data["ambient"]; got != nil {
		t.Fatalf("the forged call still produced data: %#v", got)
	}

	// And the real header DOES bind, so the refusal above is the rule and not a
	// field that simply never gets populated.
	real := ask(t, app, `{ ambient(q: "x") { org } }`, nil, map[string]string{"X-Org-Id": "real"})
	if len(real.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", real.messages())
	}
	if got := real.Data["ambient"].(map[string]any)["org"]; got != "real" {
		t.Fatalf("the request header did not reach the handler: %#v", got)
	}
}

// ★ The authorizer is the app's one gate over typed ops. If it does not run on
// this path, every service that installs one has a door around it.
func TestTheAuthorizerRunsOnTheGraphPath(t *testing.T) {
	app := resApp(t)
	var saw []string
	app.Authorize(func(ctx context.Context, op Op, in any) (Decision, error) {
		saw = append(saw, op.OperationID)
		if op.OperationID == "user" {
			return Decision{Effect: Deny, Clause: "policy", Reason: "denied by policy"}, nil
		}
		return Decision{Effect: Allow}, nil
	})

	r := ask(t, app, `{ user(id: "u1") { id email } }`, nil, nil)
	if len(saw) == 0 {
		t.Fatal("the authorizer never ran for a GraphQL field")
	}
	if r.Data["user"] != nil {
		t.Fatalf("a denied op returned data: %#v", r.Data)
	}
	if !strings.Contains(r.messages(), "denied by policy") {
		t.Errorf("the denial did not reach the caller: %s", r.messages())
	}
}

// The authorizer decides on the DECODED input, so the value it sees here must be
// the same *In the handler would bind — not a map, not the raw arguments.
func TestTheAuthorizerSeesTheTypedInput(t *testing.T) {
	app := resApp(t)
	var got *resGet
	app.Authorize(func(ctx context.Context, op Op, in any) (Decision, error) {
		if v, ok := in.(*resGet); ok {
			got = v
		}
		return Decision{Effect: Allow}, nil
	})
	ask(t, app, `{ user(id: "u7") { id } }`, nil, nil)
	if got == nil {
		t.Fatal("the authorizer did not receive the op's typed input")
	}
	if got.ID != "u7" {
		t.Errorf("the authorized value is not the value the handler binds: %#v", got)
	}
}

// Identity travels in the context, so a field must see the same caller a REST
// call would. Without this, an authorizer that keys on the caller sees nobody.
func TestTheCallerReachesTheHandler(t *testing.T) {
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	type none struct{}
	type who struct {
		Org  string `json:"org"`
		User string `json:"user"`
	}
	app.Get("/v1/whoami", func(ctx context.Context, _ *none) (*who, error) {
		c := CallerOf(ctx)
		return &who{Org: c.Org, User: c.User}, nil
	}, WithOperationID("whoami"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	r := ask(t, app, `{ whoami { org user } }`, nil,
		map[string]string{HeaderOrg: "acme", HeaderUser: "u-1"})
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	got, _ := r.Data["whoami"].(map[string]any)
	if got["org"] != "acme" || got["user"] != "u-1" {
		t.Fatalf("the caller did not reach the handler: %#v", got)
	}
}

// An op's own validation is part of its contract. Reaching it by this door must
// not skip it.
func TestTheOpsValidationStillRuns(t *testing.T) {
	r := ask(t, resApp(t), `mutation { createUser(email: "") { id } }`, nil, nil)
	if len(r.Errors) == 0 {
		t.Fatalf("a required field was accepted empty: %#v", r.Data)
	}
}

func TestVariablesBind(t *testing.T) {
	q := `query Fetch($who: String!) { user(id: $who) { id } }`
	r := ask(t, resApp(t), q, map[string]any{"who": "u9"}, nil)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	if r.Data["user"].(map[string]any)["id"] != "u9" {
		t.Fatalf("the variable did not reach the op: %#v", r.Data)
	}

	missing := ask(t, resApp(t), q, nil, nil)
	if len(missing.Errors) == 0 {
		t.Fatal("a required variable was allowed to be absent")
	}
	if missing.Data != nil {
		t.Errorf("execution began without a required variable: %#v", missing.Data)
	}
	if missing.code != http.StatusBadRequest {
		t.Errorf("a request that never executed answered %d, want 400", missing.code)
	}
}

// One field failing must not take the others with it: the answer is partial,
// and the error says which part.
func TestOneFieldFailingLeavesTheRest(t *testing.T) {
	r := ask(t, resApp(t), `{ ok: user(id: "u1") { id } bad: user(id: "gone") { id } }`, nil, nil)
	if r.Data["ok"] == nil {
		t.Fatalf("a healthy field was lost to its neighbour's failure: %#v", r.Data)
	}
	if r.Data["bad"] != nil {
		t.Fatalf("the failing field returned data: %#v", r.Data["bad"])
	}
	if len(r.Errors) != 1 {
		t.Fatalf("want exactly one error, got %d: %s", len(r.Errors), r.messages())
	}
	if len(r.Errors[0].Path) != 1 || r.Errors[0].Path[0] != "bad" {
		t.Errorf("the error does not point at the field that failed: %#v", r.Errors[0].Path)
	}
	if r.code != http.StatusOK {
		t.Errorf("a partial answer returned %d, want 200 — the data is still there", r.code)
	}
}

// Aliases are how one query calls the same field twice. Answering both under the
// field's own name would silently drop one.
func TestAliasesNameTheAnswer(t *testing.T) {
	r := ask(t, resApp(t), `{ a: user(id: "u1") { id } b: user(id: "u2") { id } }`, nil, nil)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	if r.Data["a"].(map[string]any)["id"] != "u1" || r.Data["b"].(map[string]any)["id"] != "u2" {
		t.Fatalf("aliases did not keep the two calls apart: %#v", r.Data)
	}
}

// A field absent because it was EMPTY is a null; a field absent because the type
// has no such field is a mistake. Carrying the Go type through the projection is
// what keeps those two apart — without it every typo silently returns null.
func TestAFieldTheTypeDoesNotHaveIsNamed(t *testing.T) {
	r := ask(t, resApp(t), `{ user(id: "u1") { emial } }`, nil, nil)
	if len(r.Errors) == 0 {
		t.Fatalf("a misspelt field was answered instead of refused: %#v", r.Data)
	}
	if !strings.Contains(r.messages(), "emial") {
		t.Errorf("the error does not name the field: %s", r.messages())
	}
}

func TestFragmentsSpreadAndCyclesTerminate(t *testing.T) {
	r := ask(t, resApp(t), `{ user(id: "u1") { ...bits } } fragment bits on User { id email }`, nil, nil)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	if got := r.Data["user"].(map[string]any); got["id"] != "u1" || got["email"] == nil {
		t.Fatalf("the fragment did not spread: %#v", got)
	}

	// A fragment that reaches itself would not terminate. It must be refused,
	// and the process must still be here to say so.
	loop := ask(t, resApp(t),
		`{ user(id: "u1") { ...a } } fragment a on User { id ...a }`, nil, nil)
	if len(loop.Errors) == 0 {
		t.Fatal("a self-spreading fragment was accepted")
	}
}

func TestSkipAndInclude(t *testing.T) {
	q := `query F($on: Boolean!) { user(id: "u1") { id email @include(if: $on) } }`
	off := ask(t, resApp(t), q, map[string]any{"on": false}, nil)
	if _, present := off.Data["user"].(map[string]any)["email"]; present {
		t.Errorf("@include(if: false) still answered: %#v", off.Data)
	}
	on := ask(t, resApp(t), q, map[string]any{"on": true}, nil)
	if on.Data["user"].(map[string]any)["email"] == nil {
		t.Errorf("@include(if: true) dropped the field: %#v", on.Data)
	}

	sk := ask(t, resApp(t), `{ user(id: "u1") { id email @skip(if: true) } }`, nil, nil)
	if _, present := sk.Data["user"].(map[string]any)["email"]; present {
		t.Errorf("@skip(if: true) still answered: %#v", sk.Data)
	}
}

// A directive changes what a field means, so an unknown one cannot be ignored:
// ignoring it answers a question that was not asked.
func TestAnUnknownDirectiveIsRefused(t *testing.T) {
	r := ask(t, resApp(t), `{ user(id: "u1") @cache(ttl: 60) { id } }`, nil, nil)
	if len(r.Errors) == 0 {
		t.Fatalf("an unknown directive was ignored: %#v", r.Data)
	}
	if !strings.Contains(r.messages(), "cache") {
		t.Errorf("the refusal does not name the directive: %s", r.messages())
	}
}

// Refused by name. A subscription that parsed and then found no field would
// blame the field, which is not what is wrong.
func TestSubscriptionsAreRefusedByName(t *testing.T) {
	r := ask(t, resApp(t), `subscription { user(id: "u1") { id } }`, nil, nil)
	if len(r.Errors) == 0 {
		t.Fatal("a subscription was accepted")
	}
	if !strings.Contains(r.messages(), "stream") {
		t.Errorf("the refusal does not say why: %s", r.messages())
	}
}

// Introspection is not served, and saying so is better than answering an empty
// schema — which reads to a client as "this app has no fields".
func TestIntrospectionSaysWhereTheSchemaIs(t *testing.T) {
	r := ask(t, resApp(t), `{ __schema { types { name } } }`, nil, nil)
	if len(r.Errors) == 0 {
		t.Fatal("introspection answered something")
	}
	if !strings.Contains(r.messages(), GraphPath) {
		t.Errorf("the refusal does not say where the schema is: %s", r.messages())
	}
}

// The schema and the executor share one address, so a client that can call the
// app can also learn what to call.
func TestTheSchemaIsServedAtTheSamePath(t *testing.T) {
	app := resApp(t)
	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, GraphPath, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", GraphPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s answered %d", GraphPath, resp.StatusCode)
	}
	if !strings.Contains(string(body), "type Query {") || !strings.Contains(string(body), "user(") {
		t.Fatalf("the served document is not this app's schema:\n%s", body)
	}
	if got := app.GraphQLSDL(); string(body) != got {
		t.Error("the served schema differs from the generated one")
	}
}

// A document that never ran has no data to be partially right about, and a
// client distinguishes the two cases by exactly this.
func TestAnUnparsableDocumentNeverBeginsExecution(t *testing.T) {
	r := ask(t, resApp(t), `{ user(id: "u1" `, nil, nil)
	if len(r.Errors) == 0 {
		t.Fatal("a truncated document parsed")
	}
	if r.Data != nil {
		t.Errorf("execution began on a document that does not parse: %#v", r.Data)
	}
	if r.code != http.StatusBadRequest {
		t.Errorf("answered %d, want 400", r.code)
	}
}

// An unnamed request against a multi-operation document must not be resolved by
// position: which one ran would then depend on the order they were written in.
func TestAnAmbiguousOperationIsRefused(t *testing.T) {
	doc := `query A { user(id: "u1") { id } } query B { user(id: "u2") { id } }`
	if r := ask(t, resApp(t), doc, nil, nil); len(r.Errors) == 0 {
		t.Fatalf("ran one of two operations without being told which: %#v", r.Data)
	}
	body, _ := json.Marshal(GraphRequest{Query: doc, Operation: "B"})
	req := httptest.NewRequest(http.MethodPost, GraphPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := resApp(t).Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out resReply
	_ = json.Unmarshal(raw, &out)
	if out.Data["user"].(map[string]any)["id"] != "u2" {
		t.Fatalf("operationName did not choose the operation: %s", raw)
	}
}

// A host publishes its own address space. What the projection answers is not the
// host's to change; where it answers is — the same split that puts zip's OpenAPI
// document at /.well-known and a product's at /v1.
func TestAHostCanMountTheGraphAtItsOwnAddress(t *testing.T) {
	app := resApp(t)
	app.MountGraph("/v1/graph")

	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/graph", nil))
	if err != nil {
		t.Fatalf("GET /v1/graph: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "type Query {") {
		t.Fatalf("the host's address does not serve the schema (%d):\n%s", resp.StatusCode, body)
	}

	// And it EXECUTES there, so a caller told one address needs no other.
	req := httptest.NewRequest(http.MethodPost, "/v1/graph",
		bytes.NewReader([]byte(`{"query":"{ user(id: \"u1\") { id } }"}`)))
	req.Header.Set("Content-Type", "application/json")
	run, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST /v1/graph: %v", err)
	}
	defer func() { _ = run.Body.Close() }()
	raw, _ := io.ReadAll(run.Body)
	var out resReply
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("reply is not JSON (%s): %v", raw, err)
	}
	if out.Data["user"].(map[string]any)["id"] != "u1" {
		t.Fatalf("the host's address did not run the op: %s", raw)
	}

	// zip's own address still answers: mounting one does not move the other.
	if own, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, GraphPath, nil)); err == nil {
		defer func() { _ = own.Body.Close() }()
		if own.StatusCode != http.StatusOK {
			t.Errorf("mounting a host address took %s away (%d)", GraphPath, own.StatusCode)
		}
	}
}

// A deployment that answers with many processes has ONE surface. These pin the
// seam that lets a host describe and serve that surface without a second GraphQL
// implementation: the schema and the answer come from elsewhere, everything else
// — parsing, variables, fragments, directives, projection — is the same code a
// local graph runs.

func foreignApp(t *testing.T, r Resolver) *App {
	t.Helper()
	app := New(Config{AppName: "host", DisableStartupMessage: true})
	// One local op, so the app is a real one; the foreign fields are not in it.
	app.Get("/v1/local", func(ctx context.Context, _ *resGet) (*resUser, error) {
		return &resUser{ID: "local"}, nil
	}, WithOperationID("local"))
	app.MountGraphFor("/v1/fleet", func(context.Context) (string, error) {
		return "type Query {\n  farUser(id: String!): FarUser\n}\n\ntype FarUser {\n  id: String\n  email: String\n}\n", nil
	}, r)
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

func askAt(t *testing.T, app *App, path, query string, vars ...map[string]any) resReply {
	t.Helper()
	var v map[string]any
	if len(vars) > 0 {
		v = vars[0]
	}
	body, _ := json.Marshal(GraphRequest{Query: query, Variables: v})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out resReply
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("reply is not JSON (%s): %v", raw, err)
	}
	out.code = resp.StatusCode
	return out
}

func TestAForeignFieldIsResolvedAndProjected(t *testing.T) {
	app := foreignApp(t, func(ctx context.Context, kind string, s Selection) (any, error) {
		return map[string]any{"id": s.Args["id"], "email": "far@example.com", "secret": "no"}, nil
	})
	r := askAt(t, app, "/v1/fleet", `{ farUser(id: "u1") { id email } }`)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	got, _ := r.Data["farUser"].(map[string]any)
	if got["id"] != "u1" || got["email"] != "far@example.com" {
		t.Fatalf("the resolver's answer did not survive: %#v", got)
	}
	// A selection is still a FILTER, even with nothing declaring the shape.
	if _, leaked := got["secret"]; leaked {
		t.Fatalf("a field nobody asked for was returned: %#v", got)
	}
}

// ★ The resolver is told which operation selected the field, because this app has
// no registry entry to check a write against — the rule that a write is not
// reachable as a read has to be enforceable where the knowledge is.
func TestAForeignResolverIsToldTheOperationKind(t *testing.T) {
	var kinds []string
	app := foreignApp(t, func(ctx context.Context, kind string, s Selection) (any, error) {
		kinds = append(kinds, kind)
		if kind != "mutation" {
			return nil, errors.New("that field is a write and this is a query")
		}
		return map[string]any{"id": "ok"}, nil
	})

	r := askAt(t, app, "/v1/fleet", `query { farUser(id: "u1") { id } }`)
	if len(r.Errors) == 0 {
		t.Fatalf("the resolver could not refuse a write selected as a read: %#v", r.Data)
	}
	if len(kinds) == 0 || kinds[0] != "query" {
		t.Fatalf("the resolver was not told the kind: %#v", kinds)
	}
	m := askAt(t, app, "/v1/fleet", `mutation { farUser(id: "u1") { id } }`)
	if len(m.Errors) != 0 {
		t.Fatalf("the same field refused under mutation too: %s", m.messages())
	}
}

// Variables, aliases and nesting are the executor's, not the resolver's — the
// resolver sees plain arguments and answers a plain value.
func TestAForeignResolverSeesResolvedArguments(t *testing.T) {
	var seen map[string]any
	app := foreignApp(t, func(ctx context.Context, kind string, s Selection) (any, error) {
		seen = s.Args
		return map[string]any{"id": "x", "boss": map[string]any{"id": "y", "email": "b@e"}}, nil
	})
	r := askAt(t, app, "/v1/fleet", `query F($who: String!) { a: farUser(id: $who) { boss { id } } }`,
		map[string]any{"who": "u9"})
	if seen["id"] != "u9" {
		t.Fatalf("the variable did not reach the resolver: %#v", seen)
	}
	boss, _ := r.Data["a"].(map[string]any)["boss"].(map[string]any)
	if boss["id"] != "y" {
		t.Fatalf("a nested selection was not projected: %#v", r.Data)
	}
	if _, leaked := boss["email"]; leaked {
		t.Fatalf("an unselected nested field came back: %#v", boss)
	}
}

// With no Go type there is nothing to tell a typo from an empty field, so an
// absent field is null rather than an invented error. (A LOCAL field still names
// it — see TestAFieldTheTypeDoesNotHaveIsNamed.)
func TestAnAbsentForeignFieldIsNullNotAnError(t *testing.T) {
	app := foreignApp(t, func(ctx context.Context, kind string, s Selection) (any, error) {
		return map[string]any{"id": "x"}, nil
	})
	r := askAt(t, app, "/v1/fleet", `{ farUser(id: "u1") { id email } }`)
	if len(r.Errors) != 0 {
		t.Fatalf("an absent field was reported as an error: %s", r.messages())
	}
	got, _ := r.Data["farUser"].(map[string]any)
	if got["email"] != nil {
		t.Fatalf("want null for the absent field, got %#v", got["email"])
	}
}

// A composed schema changes as the deployment does, so it is rendered per request
// rather than captured at mount.
func TestTheForeignSchemaIsRenderedPerRequest(t *testing.T) {
	n := 0
	app := New(Config{AppName: "host", DisableStartupMessage: true})
	app.Get("/v1/local", func(ctx context.Context, _ *resGet) (*resUser, error) { return nil, nil }, WithOperationID("local"))
	app.MountGraphFor("/v1/fleet", func(context.Context) (string, error) {
		n++
		return "type Query {\n  n" + strings.Repeat("x", n) + ": String\n}\n", nil
	}, nil)
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	first, _ := io.ReadAll(mustGet(t, app, "/v1/fleet"))
	second, _ := io.ReadAll(mustGet(t, app, "/v1/fleet"))
	if string(first) == string(second) {
		t.Fatalf("the schema was captured at mount, not rendered per request:\n%s", first)
	}
}

func mustGet(t *testing.T, app *App, path string) io.Reader {
	t.Helper()
	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return bytes.NewReader(b)
}

// The local graph is unchanged by any of this: same app, same door, same answer.
func TestTheLocalGraphStillAnswers(t *testing.T) {
	app := foreignApp(t, func(ctx context.Context, kind string, s Selection) (any, error) {
		return nil, errors.New("should not be asked")
	})
	app.MountGraph("/v1/own")
	r := askAt(t, app, "/v1/own", `{ local(id: "x") { id } }`)
	if len(r.Errors) != 0 {
		t.Fatalf("the local graph broke: %s", r.messages())
	}
	if r.Data["local"].(map[string]any)["id"] != "local" {
		t.Fatalf("local op did not run: %#v", r.Data)
	}
}

// ── what a resolver is handed, and what it may hand back ─────────────────────

// mountFor builds an app serving one foreign graph at /v1/g, mounted before the
// app is built — the routes are the app's own, so they are composed with it.
func mountFor(t *testing.T, sdl string, r Resolver) *App {
	t.Helper()
	app := New(Config{AppName: "host", DisableStartupMessage: true})
	app.MountGraphFor("/v1/g", func(context.Context) (string, error) { return sdl, nil }, r)
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

// rawAt posts a request and returns the status and the bytes, for assertions
// about the wire itself.
func rawAt(t *testing.T, app *App, path string, req GraphRequest, header ...string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	resp, err := app.Fiber().Test(r)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// ★ A resolver holds the whole of what was asked below its field: fragments
// spread, skipped fields gone, variables in place, aliases kept. That is what
// lets one that fronts a planner fetch only what is selected.
func TestAResolverReceivesTheGatheredSelection(t *testing.T) {
	var got Selection
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		got = s
		return map[string]any{}, nil
	})
	q := `query Q($n: Int, $off: Boolean!) {
		top: a(x: 1) {
			...F
			b(n: $n) { c }
			gone @skip(if: $off)
			... @skip(if: $off) { also }
		}
	}
	fragment F on A { first: d e }`
	r := askAt(t, app, "/v1/g", q, map[string]any{"n": 7, "off": true})
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	if got.Name != "a" || got.Alias != "top" || got.Key() != "top" {
		t.Fatalf("the field is named %q under %q, want a under top", got.Name, got.Key())
	}
	if got.Args["x"] != int64(1) {
		t.Fatalf("args = %#v", got.Args)
	}
	var keys []string
	for _, f := range got.Fields {
		keys = append(keys, f.Key())
	}
	if strings.Join(keys, ",") != "first,e,b" {
		t.Fatalf("selection = %v, want the fragment spread, the skipped field and the inline fragment gone", keys)
	}
	b := got.Fields[2]
	if b.Args["n"] != float64(7) || len(b.Fields) != 1 || b.Fields[0].Name != "c" {
		t.Fatalf("the nested field lost its variable or its selection: %#v", b)
	}
	if got.Depth() != 3 {
		t.Errorf("Depth = %d, want 3 (a, b, c)", got.Depth())
	}
}

// A declared variable that was given nothing leaves its argument out, as the
// specification says; a reference to one the operation never declared is
// refused before anything runs; and a variable given and never declared is
// ignored, because nothing can refer to it.
func TestVariablesTheRequestDidNotSettle(t *testing.T) {
	var got Selection
	app := mountFor(t, "type Query { a(n: Int): JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		got = s
		return "ok", nil
	})
	if r := askAt(t, app, "/v1/g", `query($n: Int) { a(n: $n) }`); len(r.Errors) != 0 {
		t.Fatalf("an optional variable left unset failed: %s", r.messages())
	}
	if _, present := got.Args["n"]; present {
		t.Errorf("the unset variable reached the resolver as %#v", got.Args)
	}
	r := askAt(t, app, "/v1/g", `{ a(n: $nope) }`)
	if r.code != http.StatusBadRequest || r.Data != nil {
		t.Errorf("an undeclared variable was run (%d): %#v", r.code, r.Data)
	}
	if r := askAt(t, app, "/v1/g", `{ a }`, map[string]any{"extra": 1}); len(r.Errors) != 0 {
		t.Errorf("a variable the operation does not declare was refused: %s", r.messages())
	}
	if r := askAt(t, app, "/v1/g", `query($n: Int = 3) { a(n: $n) }`); len(r.Errors) != 0 || got.Args["n"] != int64(3) {
		t.Errorf("the default did not apply: %v %s", got.Args, r.messages())
	}
}

// Fields answering under one key are one field: their selections join, and the
// resolver runs once. Two different fields under one key cannot both answer and
// are refused rather than resolved by whichever came last.
func TestFieldsUnderOneKeyMerge(t *testing.T) {
	calls := 0
	var got Selection
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		calls++
		got = s
		return map[string]any{"x": 1, "y": 2}, nil
	})
	r := askAt(t, app, "/v1/g", `{ a { x } ...F } fragment F on Query { a { y } }`)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	if calls != 1 || len(got.Fields) != 2 {
		t.Fatalf("calls = %d, selection = %#v; want one call selecting x and y", calls, got.Fields)
	}
	if m := r.Data["a"].(map[string]any); m["x"] != float64(1) || m["y"] != float64(2) {
		t.Fatalf("the merged field lost half its selection: %#v", m)
	}
	clash := askAt(t, app, "/v1/g", `{ a: a { x } a: b }`)
	if clash.code != http.StatusBadRequest || len(clash.Errors) == 0 {
		t.Fatalf("two fields under one key were run (%d): %#v", clash.code, clash.Data)
	}
}

// A fragment that reaches itself, through any chain, and a spread of a fragment
// nobody wrote are refused before any field resolves.
func TestABrokenFragmentNeverRuns(t *testing.T) {
	ran := false
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		ran = true
		return map[string]any{}, nil
	})
	for _, q := range []string{
		`{ a { ...A } } fragment A on T { ...B } fragment B on T { x ...A }`,
		`{ a { ...Missing } }`,
		`{ a } fragment A on T { ...A }`,
	} {
		r := askAt(t, app, "/v1/g", q)
		if r.code != http.StatusBadRequest || r.Data != nil || len(r.Errors) == 0 {
			t.Errorf("%s: answered %d with %#v", q, r.code, r.Data)
		}
	}
	if ran {
		t.Error("a field resolved for a document that cannot be expanded")
	}
}

// Fragments that double at every link would be a small document asking for an
// enormous answer. The count is taken while the request is gathered, so the
// refusal comes before anything is spent.
func TestAnExpansionPastTheBoundIsRefused(t *testing.T) {
	ran := false
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		ran = true
		return map[string]any{}, nil
	})
	var b strings.Builder
	b.WriteString("{ a { ...F0 } }\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "fragment F%d on T { l { ...F%d } r { ...F%d } }\n", i, i+1, i+1)
	}
	b.WriteString("fragment F20 on T { leaf }\n")
	r := askAt(t, app, "/v1/g", b.String())
	if r.code != http.StatusBadRequest || ran {
		t.Fatalf("a 2^20 expansion was run (%d, ran=%v): %s", r.code, ran, r.messages())
	}
}

// The width of a request is bounded, and the bound refuses rather than truncates.
func TestTheRootIsBounded(t *testing.T) {
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		return "ok", nil
	})
	query := func(n int) string {
		var b strings.Builder
		b.WriteString("{")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, " f%d: a", i)
		}
		return b.String() + " }"
	}
	if r := askAt(t, app, "/v1/g", query(rootLimit)); len(r.Errors) != 0 || len(r.Data) != rootLimit {
		t.Fatalf("%d root fields: %s", rootLimit, r.messages())
	}
	r := askAt(t, app, "/v1/g", query(rootLimit+1))
	if r.code != http.StatusBadRequest || r.Data != nil {
		t.Fatalf("%d root fields were run (%d)", rootLimit+1, r.code)
	}
}

// ★ The root fields of a query resolve at once, so a request costs its slowest
// field; a mutation's run one after another, in the order written.
func TestQueriesRunAtOnceAndMutationsInOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		order = append(order, s.Key())
		mu.Unlock()
		return s.Key(), nil
	})
	start := time.Now()
	if r := askAt(t, app, "/v1/g", `{ a1: a a2: a a3: a a4: a }`); len(r.Errors) != 0 {
		t.Fatalf("query: %s", r.messages())
	}
	if took := time.Since(start); took > 450*time.Millisecond {
		t.Errorf("four 150ms fields took %v: the query ran them one after another", took)
	}
	order = nil
	start = time.Now()
	if r := askAt(t, app, "/v1/g", `mutation { m1: a m2: a m3: a }`); len(r.Errors) != 0 {
		t.Fatalf("mutation: %s", r.messages())
	}
	if took := time.Since(start); took < 450*time.Millisecond {
		t.Errorf("three 150ms mutation fields took %v: they overlapped", took)
	}
	if strings.Join(order, ",") != "m1,m2,m3" {
		t.Errorf("mutation fields ran as %v, want the order written", order)
	}
}

// A field that panics on its own goroutine fails that field, not the process,
// and what it panicked with stays in the log.
func TestAPanickingFieldFailsAlone(t *testing.T) {
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		if s.Key() == "bad" {
			panic("boom")
		}
		return "ok", nil
	})
	r := askAt(t, app, "/v1/g", `{ good: a bad: a }`)
	if r.Data["good"] != "ok" || r.Data["bad"] != nil || !strings.Contains(r.messages(), "internal error (request ") ||
		strings.Contains(r.messages(), "boom") {
		t.Fatalf("data %#v, errors %s", r.Data, r.messages())
	}
}

// ★ An answer's keys come in the order the request selected them, as the
// specification asks — not sorted, which is what a map would give.
func TestTheAnswerKeepsTheSelectionsOrder(t *testing.T) {
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		return map[string]any{"z": 1, "m": map[string]any{"b": 1, "a": 2}, "a": 3}, nil
	})
	_, raw := rawAt(t, app, "/v1/g", GraphRequest{Query: `{ second: a { z m { b a } a } first: a { a } }`})
	want := `{"data":{"second":{"z":1,"m":{"b":1,"a":2},"a":3},"first":{"a":3}}}`
	if raw != want {
		t.Fatalf("wire:\n got %s\nwant %s", raw, want)
	}
}

// ── objects: a value that answers its own fields ─────────────────────────────

type gnode struct {
	key   string
	asked *[]string
}

func (n gnode) Type() string { return "Node" }

func (n gnode) Field(ctx context.Context, s Selection) (any, error) {
	*n.asked = append(*n.asked, n.key+"."+s.Key())
	switch s.Name {
	case "key":
		return n.key, nil
	case "next":
		var in struct {
			Step int `json:"step" validate:"required"`
		}
		if err := s.Bind(&in); err != nil {
			return nil, err
		}
		return []*gnode{{key: fmt.Sprintf("%s+%d", n.key, in.Step), asked: n.asked}}, nil
	case "none":
		return (*gnode)(nil), nil
	case "data":
		return map[string]any{"x": 1, "y": 2}, nil
	}
	return nil, ErrBadRequest(fmt.Sprintf("no field %q on Node", s.Name))
}

// ★ Nesting is traversal: each object is asked for the fields selected on it,
// with that field's own arguments, so two aliases of one field with different
// arguments are two answers, and a field nobody selected is never computed.
func TestAnObjectAnswersItsOwnFields(t *testing.T) {
	var asked []string
	app := mountFor(t, "type Query { root: Node }", func(ctx context.Context, kind string, s Selection) (any, error) {
		return gnode{key: "r", asked: &asked}, nil
	})
	r := askAt(t, app, "/v1/g", `{ root {
		__typename
		key
		one: next(step: 1) { key }
		two: next(step: 2) { key next(step: 3) { key } }
		none { key }
		data { y }
	} }`)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected errors: %s", r.messages())
	}
	root := r.Data["root"].(map[string]any)
	if root["__typename"] != "Node" || root["key"] != "r" {
		t.Fatalf("root = %#v", root)
	}
	one := root["one"].([]any)[0].(map[string]any)
	two := root["two"].([]any)[0].(map[string]any)
	if one["key"] != "r+1" || two["key"] != "r+2" {
		t.Fatalf("the aliases did not keep their arguments apart: one=%#v two=%#v", one, two)
	}
	if deep := two["next"].([]any)[0].(map[string]any); deep["key"] != "r+2+3" {
		t.Fatalf("the second hop did not resolve: %#v", deep)
	}
	if root["none"] != nil {
		t.Errorf("a nil object answered %#v, want null", root["none"])
	}
	if d := root["data"].(map[string]any); d["y"] != float64(2) || d["x"] != nil {
		t.Errorf("plain data under an object was not projected: %#v", d)
	}
	for _, a := range asked {
		if strings.HasSuffix(a, ".__typename") {
			t.Errorf("__typename was asked of the object (%s) rather than read off its Type", a)
		}
	}
	if len(asked) != 9 {
		t.Errorf("the objects were asked %d times (%v), want once per selected field", len(asked), asked)
	}
}

// An object's field failing fails that field, at its own path, and its siblings
// answer. An object with nothing selected on it, and a scalar with a selection,
// are named rather than answered.
func TestAnObjectsFailuresAreItsFieldsAlone(t *testing.T) {
	var asked []string
	app := mountFor(t, "type Query { root: Node }", func(ctx context.Context, kind string, s Selection) (any, error) {
		return gnode{key: "r", asked: &asked}, nil
	})
	r := askAt(t, app, "/v1/g", `{ root { key nope next { key } } }`)
	if root := r.Data["root"].(map[string]any); root["key"] != "r" || root["nope"] != nil || root["next"] != nil {
		t.Fatalf("root = %#v", root)
	}
	if len(r.Errors) != 2 {
		t.Fatalf("errors = %s, want the unknown field and the missing argument", r.messages())
	}
	for _, e := range r.Errors {
		if len(e.Path) != 2 || e.Path[0] != "root" {
			t.Errorf("error %q is at %v, want under root", e.Message, e.Path)
		}
	}
	if !strings.Contains(r.messages(), `"step" of "next" is required`) {
		t.Errorf("Bind did not apply the validate tag: %s", r.messages())
	}

	for q, want := range map[string]string{
		`{ root }`:                                     "needs a selection",
		`{ root { next(step: 1) } }`:                   "needs a selection",
		`{ root { key { x } } }`:                       "cannot have a selection",
		`{ root { next(step: 1, extra: 2) { key } } }`: `unknown argument "extra"`,
		`{ root { next(step: "x") { key } } }`:         "takes Int",
	} {
		if got := askAt(t, app, "/v1/g", q); !strings.Contains(got.messages(), want) {
			t.Errorf("%s: errors %q, want one saying %q", q, got.messages(), want)
		}
	}
}

// ── where the graph lives, and what it is told about the caller ─────────────

// ★ The schema is rendered for the caller asking, so a schema may differ by who
// is asking — an org's own fields are that org's schema.
func TestTheSchemaIsRenderedForTheCaller(t *testing.T) {
	app := New(Config{AppName: "host", DisableStartupMessage: true})
	app.MountGraphFor("/v1/g", func(ctx context.Context) (string, error) {
		return "type Query { for_" + CallerOf(ctx).Org + ": String }", nil
	}, nil)
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, org := range []string{"acme", "zeta"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/g", nil)
		req.Header.Set(HeaderOrg, org)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !strings.Contains(string(b), "for_"+org) {
			t.Errorf("the schema %s was asked for did not see the caller: %s", org, b)
		}
	}
}

// A schema that cannot render is a 500 that says so, not an empty schema, and
// why it could not is the log's, under the id the answer names.
func TestASchemaThatFailsSaysSo(t *testing.T) {
	app := New(Config{AppName: "host", DisableStartupMessage: true})
	app.MountGraphFor("/v1/g", func(context.Context) (string, error) {
		return "", errors.New("the composition failed")
	}, nil)
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/g", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	rid := resp.Header.Get(HeaderRequestID)
	if resp.StatusCode != http.StatusInternalServerError || rid == "" || !strings.Contains(string(b), rid) ||
		strings.Contains(string(b), "the composition failed") {
		t.Fatalf("answered %d (request %q): %s", resp.StatusCode, rid, b)
	}
}

// A foreign graph's routes are the app's own and are declared, so a host routes
// to them and a document describes them; zip's projection of the app's own ops
// is control plane and is not.
func TestAForeignGraphIsDeclaredAndTheProjectionIsNot(t *testing.T) {
	app := New(Config{AppName: "host", DisableStartupMessage: true})
	app.Get("/v1/local", func(ctx context.Context, _ *resGet) (*resUser, error) { return nil, nil }, WithOperationID("local"))
	app.MountGraphFor("/v1/g", func(context.Context) (string, error) { return "", nil }, nil)
	app.MountGraph("/v1/own")
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		if !app.Declares(m, "/v1/g") {
			t.Errorf("%s /v1/g is not declared", m)
		}
		if app.Declares(m, "/v1/own") {
			t.Errorf("%s /v1/own, zip's projection, is declared", m)
		}
	}
}

// Introspection is refused with the address the schema IS at — the mount's own,
// not zip's control plane, which a foreign graph does not live at.
func TestIntrospectionNamesTheMountsAddress(t *testing.T) {
	app := mountFor(t, "type Query { a: JSON }", func(context.Context, string, Selection) (any, error) { return nil, nil })
	r := askAt(t, app, "/v1/g", `{ __schema { types { name } } __typename }`)
	if !strings.Contains(r.messages(), "GET /v1/g") {
		t.Errorf("the refusal does not say where the schema is: %s", r.messages())
	}
	if r.Data["__typename"] != "Query" {
		t.Errorf("__typename = %#v, want Query", r.Data["__typename"])
	}
}

// Headers is the request whole, for a resolver that relays it: the credential,
// the cookies, and what the edge set.
func TestHeadersRelaysTheRequest(t *testing.T) {
	var got http.Header
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		got = Headers(ctx)
		return "ok", nil
	})
	rawAt(t, app, "/v1/g", GraphRequest{Query: `{ a }`},
		"Authorization", "Bearer t", "Cookie", "s=1", HeaderOrg, "acme")
	if got.Get("Authorization") != "Bearer t" || got.Get("Cookie") != "s=1" || got.Get(HeaderOrg) != "acme" {
		t.Fatalf("headers = %v", got)
	}
	if Headers(context.Background()) != nil {
		t.Error("a context with no request behind it reported headers")
	}
}

// ── what one request may cost, and what a failure tells the caller ───────────

// hub is a node of a fully connected graph: every hub's `next` is every hub.
// Each Field call is counted, which is the work a real Object would do.
type hub struct {
	id    int
	all   int
	calls *atomic.Int64
}

func (h hub) Type() string { return "Hub" }

func (h hub) Field(ctx context.Context, s Selection) (any, error) {
	h.calls.Add(1)
	switch s.Name {
	case "id":
		return h.id, nil
	case "next":
		out := make([]hub, h.all)
		for i := range out {
			out[i] = hub{id: i, all: h.all, calls: h.calls}
		}
		return out, nil
	}
	return nil, ErrBadRequest("no field " + s.Name)
}

// ★ A small request must not be able to make the engine resolve without end.
// Thirteen fully connected hubs, `next` nested four deep, is 13^4 = 28,561
// objects from a document of a few dozen bytes: the static bounds cannot see it,
// because the fan-out is in the data. The engine counts what it resolves and
// refuses the request whole once the count passes the bound, so the work spent is
// the bound's, not the graph's.
func TestAFanOutPastTheBudgetIsRefused(t *testing.T) {
	var calls atomic.Int64
	app := mountFor(t, "type Query { root: Hub }", func(ctx context.Context, kind string, s Selection) (any, error) {
		return hub{all: 13, calls: &calls}, nil
	})
	deep := askAt(t, app, "/v1/g", `{ root { next { next { next { next { id } } } } } }`)
	if deep.code != http.StatusBadRequest || deep.Data != nil {
		t.Fatalf("a 28,561-object answer was given (%d)", deep.code)
	}
	if !strings.Contains(deep.messages(), "resolutions") {
		t.Errorf("the refusal does not say which bound: %s", deep.messages())
	}
	if n := calls.Load(); n > fieldLimit {
		t.Errorf("the objects were asked %d times; the bound is %d", n, fieldLimit)
	}

	calls.Store(0)
	ok := askAt(t, app, "/v1/g", `{ root { next { next { id } } } }`)
	if len(ok.Errors) != 0 || ok.code != http.StatusOK {
		t.Fatalf("a 169-object answer was refused: %s", ok.messages())
	}
}

// Every query root field is a resolver's call — for a host, a hop to another
// process — so at most rootParallel run at once, however many are selected.
func TestRootFieldsRunAFewAtATime(t *testing.T) {
	var now, most atomic.Int64
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		n := now.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		now.Add(-1)
		return "ok", nil
	})
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, " f%d: a", i)
	}
	b.WriteString(" }")
	if r := askAt(t, app, "/v1/g", b.String()); len(r.Errors) != 0 {
		t.Fatalf("errors: %s", r.messages())
	}
	if m := most.Load(); m > rootParallel || m < 2 {
		t.Errorf("%d root fields ran at once; want between 2 and %d", m, rootParallel)
	}
}

// ★ What a resolver meant the caller to read — a refusal with a 4xx status —
// is what the caller reads. Anything else is the server's own business: an
// address, a path, a panic's value. It is logged with the request's id, and the
// caller is given the id to quote.
func TestAnInternalFailureIsAnIDNotItsDetail(t *testing.T) {
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		switch s.Key() {
		case "meant":
			return nil, ErrForbidden("not yours to read")
		case "boom":
			panic("secret panic value")
		}
		return nil, errors.New("dial unix /run/secret/graph.sock: connection refused")
	})
	body, _ := json.Marshal(GraphRequest{Query: `{ meant: a internal: a boom: a }`})
	req := httptest.NewRequest(http.MethodPost, "/v1/g", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	rid := resp.Header.Get(HeaderRequestID)
	if rid == "" {
		t.Fatal("the answer carries no request id to quote")
	}
	got := string(raw)
	for _, leak := range []string{"secret", "dial unix", "/run/"} {
		if strings.Contains(got, leak) {
			t.Errorf("the answer leaks %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "not yours to read") {
		t.Errorf("a refusal meant for the caller was hidden: %s", got)
	}
	if strings.Count(got, rid) != 2 {
		t.Errorf("the two internal failures do not name request %s: %s", rid, got)
	}

	// A request that brings its own id keeps it, so the caller's trace holds.
	req = httptest.NewRequest(http.MethodPost, "/v1/g", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderRequestID, "trace-123")
	resp, _ = app.Fiber().Test(req)
	raw, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.Header.Get(HeaderRequestID) != "trace-123" || !strings.Contains(string(raw), "trace-123") {
		t.Errorf("the caller's own request id was replaced: %s %s", resp.Header.Get(HeaderRequestID), raw)
	}
}

// An argument of the wrong type is named with the type it takes, in the
// schema's words, not the decoder's.
func TestBindSaysWhatAnArgumentTakes(t *testing.T) {
	var asked []string
	app := mountFor(t, "type Query { root: Node }", func(ctx context.Context, kind string, s Selection) (any, error) {
		return gnode{key: "r", asked: &asked}, nil
	})
	r := askAt(t, app, "/v1/g", `{ root { next(step: "x") { key } } }`)
	msg := r.messages()
	if !strings.Contains(msg, `argument "step" of "next" takes Int`) || strings.Contains(msg, "Go") ||
		strings.Contains(msg, "unmarshal") {
		t.Errorf("message = %q", msg)
	}
	r = askAt(t, app, "/v1/g", `{ root { next { key } } }`)
	if !strings.Contains(r.messages(), `argument "step" of "next" is required`) {
		t.Errorf("message = %q", r.messages())
	}
}

// A graph mounted on a group is the group's: its middleware runs in front of
// the graph, and the address the schema is said to live at is the whole one.
func TestAGroupMountsAGraphBehindItsMiddleware(t *testing.T) {
	app := New(Config{AppName: "host", DisableStartupMessage: true})
	saw := 0
	g := app.Group("/v1/kg", func(c *Ctx) error {
		saw++
		return c.Next()
	})
	g.MountGraphFor("/graphql", func(context.Context) (string, error) { return "type Query { a: JSON }", nil },
		func(context.Context, string, Selection) (any, error) { return "ok", nil })
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	r := askAt(t, app, "/v1/kg/graphql", `{ a __schema { types { name } } }`)
	if r.Data["a"] != "ok" || saw != 1 {
		t.Fatalf("data %#v, middleware ran %d times", r.Data, saw)
	}
	if !strings.Contains(r.messages(), "GET /v1/kg/graphql") {
		t.Errorf("the schema's address is not the whole one: %s", r.messages())
	}
	if !app.Declares(http.MethodGet, "/v1/kg/graphql") || !app.Declares(http.MethodPost, "/v1/kg/graphql") {
		t.Error("the group's graph is not declared at its whole address")
	}
}

// Identity is what a Call made with the context would carry: the caller the
// gateway asserted, or the org an ActingAs re-points it to, and nothing a
// relayed request could add beside it.
func TestIdentityIsWhatACallCarries(t *testing.T) {
	var plain, acting http.Header
	app := mountFor(t, "type Query { a: JSON }", func(ctx context.Context, kind string, s Selection) (any, error) {
		plain = Identity(ctx)
		as, err := ActingAs(ctx, "other")
		if err != nil {
			return nil, err
		}
		acting = Identity(as)
		return "ok", nil
	})
	rawAt(t, app, "/v1/g", GraphRequest{Query: `{ a }`},
		HeaderOrg, "acme", HeaderUser, "acme/z", HeaderRequestID, "rid-1", "X-Workspace-Id", "w")
	if plain.Get(HeaderOrg) != "acme" || plain.Get(HeaderUser) != "acme/z" || plain.Get(HeaderRequestID) != "rid-1" {
		t.Fatalf("identity = %v", plain)
	}
	if plain.Get("X-Workspace-Id") != "" {
		t.Errorf("a header that is not zip's identity rode along: %v", plain)
	}
	if acting.Get(HeaderOrg) != "other" || acting.Get(HeaderActedBy) == "" {
		t.Errorf("an ActingAs identity = %v", acting)
	}
	if len(Identity(context.Background())) != 0 {
		t.Error("a context with no caller carries an identity")
	}
}

type shelf struct {
	Books []book `json:"books"`
}

type book struct {
	Title string  `json:"title"`
	Note  *string `json:"note,omitempty"`
}

// A list of objects is projected member by member against its element's type, on
// a local op's answer and on a resolver's. A resolver's typed answer is checked
// like a local one: a field the type does not have is named, and __typename is
// the type's.
func TestAListOfObjectsProjects(t *testing.T) {
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	app.Get("/v1/shelf", func(ctx context.Context, _ *struct{}) (*shelf, error) {
		return &shelf{Books: []book{{Title: "a"}, {Title: "b"}}}, nil
	}, WithOperationID("shelf"))
	app.MountGraphFor("/v1/g", func(context.Context) (string, error) { return "", nil },
		func(context.Context, string, Selection) (any, error) { return []book{{Title: "c"}}, nil })
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	local := app.GraphQL(context.Background(), GraphRequest{Query: `{ shelf { books { title } } }`})
	if len(local.Errors) != 0 {
		t.Fatalf("a list of objects in a local answer: %v", local.Errors)
	}
	books := local.Data.(map[string]any)["shelf"].(map[string]any)["books"].([]any)
	if len(books) != 2 || books[1].(map[string]any)["title"] != "b" {
		t.Fatalf("books = %#v", books)
	}
	if r := app.GraphQL(context.Background(), GraphRequest{Query: `{ shelf { books } }`}); len(r.Errors) != 1 {
		t.Errorf("a list of objects with nothing selected: %v, want one error", r.Errors)
	}

	r := askAt(t, app, "/v1/g", `{ a { title note __typename } }`)
	row := r.Data["a"].([]any)[0].(map[string]any)
	if len(r.Errors) != 0 || row["title"] != "c" || row["note"] != nil || row["__typename"] != "Book" {
		t.Fatalf("a typed resolver answer: %#v %s", row, r.messages())
	}
	if bad := askAt(t, app, "/v1/g", `{ a { titel } }`); !strings.Contains(bad.messages(), "titel") {
		t.Errorf("a field the type does not have was answered: %s", bad.messages())
	}
}
