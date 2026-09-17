package zip_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

type acct struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type acctReq struct {
	Name string `json:"name"`
}

// api is the shape a service registers: methods on a value, passed by name.
type api struct{ made int }

// CreateAccount opens an account.
func (a *api) CreateAccount(ctx context.Context, in *acctReq) (*acct, error) {
	a.made++
	return &acct{ID: "acct_1", Name: in.Name}, nil
}

// GetAccount reads one account.
func (a *api) GetAccount(ctx context.Context, in *struct{}) (*acct, error) {
	return &acct{ID: "acct_1", Name: "held"}, nil
}

func newApp(t *testing.T) *zip.App {
	t.Helper()
	return zip.New(zip.Config{AppName: "scoped", DisableStartupMessage: true})
}

// The verb methods are generic METHODS, so In and Out come from the handler and
// the declaration carries no type arguments at all.
func TestScope_InfersFromTheHandler(t *testing.T) {
	app, a := newApp(t), &api{}
	accounts := app.Scope("/v1/accounts").Tag("accounts")
	accounts.Post("/", a.CreateAccount).ID("accounts.create").Summary("Open an account")
	accounts.Get("/:id", a.GetAccount)

	resp, err := app.Fiber().Test(httptest.NewRequest("POST", "/v1/accounts/", strings.NewReader(`{"name":"ada"}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("POST /v1/accounts/ = %d, want 200", resp.StatusCode)
	}
	var got acct
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "ada" || a.made != 1 {
		t.Fatalf("handler ran as %+v (made=%d); the bound method must be what serves", got, a.made)
	}
}

// A nested scope composes prefixes, and the route answers at the whole path.
func TestScope_Nests(t *testing.T) {
	app, a := newApp(t), &api{}
	v1 := app.Scope("/v1")
	v1.Scope("/accounts").Get("/:id", a.GetAccount)

	resp, err := app.Fiber().Test(httptest.NewRequest("GET", "/v1/accounts/acct_1", nil))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET /v1/accounts/acct_1 = %d, want 200", resp.StatusCode)
	}
}

// The scope's declarations and the operation's chain both reach the one record
// every projection reads, so the document says what the code declared.
func TestScope_MetadataReachesTheDocument(t *testing.T) {
	app, a := newApp(t), &api{}
	app.Scope("/v1/accounts").Tag("accounts").
		Post("/", a.CreateAccount).ID("accounts.create").Summary("Open an account").Tag("write")

	doc, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("openapi: %v", err)
	}
	for _, want := range []string{"accounts.create", "Open an account", `"accounts"`, `"write"`} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("document does not carry %s", want)
		}
	}
}

// The low-level form and the scope form are the same registration, so a service
// may hold either and both are projected.
func TestScope_MatchesThePackageLevelForm(t *testing.T) {
	a := &api{}
	viaScope := newApp(t)
	viaScope.Scope("/v1/accounts").Post("/", a.CreateAccount)

	viaFunc := newApp(t)
	zip.Post[acctReq, acct](viaFunc.Group("/v1/accounts"), "/", a.CreateAccount)

	s, err := json.Marshal(viaScope.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	f, err := json.Marshal(viaFunc.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	if string(s) != string(f) {
		t.Fatalf("the two forms publish different documents:\n scope: %s\n func:  %s", s, f)
	}
}
