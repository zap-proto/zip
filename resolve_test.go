package zip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	Get(app, "/v1/users/:id", func(ctx context.Context, in *resGet) (*resUser, error) {
		if in.ID == "gone" {
			return nil, errors.New("no such user")
		}
		return &resUser{ID: in.ID, Email: in.ID + "@example.com",
			Boss: &resHum{Name: "ada", Rank: 1}}, nil
	}, WithOperationID("user"), WithSummary("One user by id"))
	Post(app, "/v1/users", func(ctx context.Context, in *resMake) (*resUser, error) {
		return &resUser{ID: "new", Email: in.Email}, nil
	}, WithOperationID("createUser"))
	Get(app, "/v1/ambient", func(ctx context.Context, in *resAmb) (*resWho, error) {
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
	app.Authorize(func(ctx context.Context, op Op, in any) error {
		saw = append(saw, op.OperationID)
		if op.OperationID == "user" {
			return errors.New("denied by policy")
		}
		return nil
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
	app.Authorize(func(ctx context.Context, op Op, in any) error {
		if v, ok := in.(*resGet); ok {
			got = v
		}
		return nil
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
	Get(app, "/v1/whoami", func(ctx context.Context, _ *none) (*who, error) {
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
