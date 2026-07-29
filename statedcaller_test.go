package zip_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// The identity plane has one job: the principal a callee acts on is the same
// one whether the request arrived over REST or over a call, and no caller can
// promote itself along the way. These exercise the three ways that can break.

type whoIn struct{}

type whoOut struct {
	Org      string `json:"org"`
	Project  string `json:"project"`
	User     string `json:"user"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Owner    string `json:"owner"`
	Admin    bool   `json:"admin"`
	OrgAdmin bool   `json:"orgAdmin"`
}

func whoApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "who"})
	zip.Post[whoIn, whoOut](app, "/v1/who", func(ctx context.Context, _ *whoIn) (*whoOut, error) {
		c := zip.CallerOf(ctx)
		return &whoOut{
			Org: c.Org, Project: c.Project, User: c.User, Name: c.Name,
			Email: c.Email, Owner: c.Owner, Admin: c.Admin, OrgAdmin: c.OrgAdmin,
		}, nil
	}, zip.WithOperationID("who_ask"))
	return app
}

// dialWho serves the app on its own unix socket and dials it back.
func dialWho(t *testing.T) *zip.Conn {
	t.Helper()
	c, err := zip.Dial(serveUDS(t, whoApp(t)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// A stated caller reaches the callee WHOLE. The subset bug is the dangerous
// one: a plane that forwarded org and dropped owner would have every callee
// decide privilege differently over a call than over REST, and nothing would
// say so.
func TestStatedCallerCrossesWhole(t *testing.T) {
	conn := dialWho(t)

	want := zip.Caller{
		Org: "acme", Project: "proj-1", User: "u-7", Name: "ada",
		Email: "ada@acme.test", Owner: "acme", Admin: true, OrgAdmin: true,
	}
	ctx := zip.WithCaller(context.Background(), want)
	got, err := zip.Call[whoIn, whoOut](ctx, conn, "who_ask", &whoIn{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Org != want.Org || got.Project != want.Project || got.User != want.User ||
		got.Name != want.Name || got.Email != want.Email || got.Owner != want.Owner ||
		!got.Admin || !got.OrgAdmin {
		t.Fatalf("identity did not cross whole:\n got %+v\nwant %+v", got, want)
	}
}

// The two admin scopes must not bleed into one another. An org admin of their
// own org is not platform sudo, and a plane that forwarded IsOrgAdmin into
// IsAdmin (or inferred either from the other) would hand a tenant's own admin
// the cross-tenant surfaces.
func TestOrgAdminIsNotPlatformAdmin(t *testing.T) {
	conn := dialWho(t)

	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", Owner: "acme", OrgAdmin: true})
	got, err := zip.Call[whoIn, whoOut](ctx, conn, "who_ask", &whoIn{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Admin {
		t.Fatal("an org admin arrived as a platform admin")
	}
	if !got.OrgAdmin {
		t.Fatal("the org-admin scope was dropped")
	}
	if got.Owner != "acme" {
		t.Fatalf("owner = %q, want acme", got.Owner)
	}
}

// A stated caller reads back off its own context. Write and read are the same
// value, so a background job sees the identity it is about to send rather than
// discovering it one hop later.
func TestStatedCallerReadsBack(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-7"})
	if c := zip.CallerOf(ctx); c.Org != "acme" || c.User != "u-7" {
		t.Fatalf("CallerOf = %+v, want org acme user u-7", c)
	}
	if c := zip.CallerOf(context.Background()); c != (zip.Caller{}) {
		t.Fatalf("a bare context claimed an identity: %+v", c)
	}
}

// The FORWARDED path — a real inbound request, propagated onward with
// Ctx.Forward. This is the one the identityHeaders list governs, and the one a
// narrowed list breaks: drop a header from that array and the callee decides on
// a principal missing exactly the field that was dropped.
func TestForwardedIdentityCrossesWhole(t *testing.T) {
	callee := dialWho(t)

	front := zip.New(zip.Config{AppName: "front"})
	front.Get("/ask", func(c *zip.Ctx) error {
		out, err := zip.Call[whoIn, whoOut](c.Forward(), callee, "who_ask", &whoIn{})
		if err != nil {
			return err
		}
		return c.JSON(200, out)
	})

	req := httptest.NewRequest("GET", "/ask", nil)
	for h, v := range map[string]string{
		zip.HeaderOrg: "acme", zip.HeaderProject: "proj-1",
		zip.HeaderUser: "u-7", zip.HeaderUserName: "ada",
		zip.HeaderUserEmail: "ada@acme.test", zip.HeaderUserOwner: "acme",
		zip.HeaderUserAdmin: "true", zip.HeaderUserOrgAdmin: "true",
	} {
		req.Header.Set(h, v)
	}
	resp, err := front.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got whoOut
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := whoOut{
		Org: "acme", Project: "proj-1", User: "u-7", Name: "ada",
		Email: "ada@acme.test", Owner: "acme", Admin: true, OrgAdmin: true,
	}
	if got != want {
		t.Fatalf("forwarded identity lost fields:\n got %+v\nwant %+v", got, want)
	}
}

// Nothing stated: nothing sent. An unattributed call must look unattributed —
// a callee that admits an empty org because it read one that was never
// asserted is the failure this preserves against.
func TestUnstatedCallerStaysAnonymous(t *testing.T) {
	conn := dialWho(t)

	got, err := zip.Call[whoIn, whoOut](context.Background(), conn, "who_ask", &whoIn{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Org != "" || got.User != "" || got.Owner != "" || got.Admin || got.OrgAdmin {
		t.Fatalf("an unattributed call carried an identity: %+v", got)
	}
}
