package zip_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zap-proto/zip"
)

type declIn struct {
	Ref string `json:"ref"`
}
type declOut struct {
	Total string `json:"total"`
}

func declApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "demo", DisableStartupMessage: true})
	zip.Post(app, "/v1/demo/quote", func(ctx context.Context, in *declIn) (*declOut, error) {
		return &declOut{Total: "1.00"}, nil
	}, zip.WithOperationID("demo_quote"))
	zip.Get(app, "/v1/demo/quotes/:id", func(ctx context.Context, in *declIn) (*declOut, error) {
		return &declOut{Total: "2.00"}, nil
	}, zip.WithOperationID("demo_quote_read"))
	// An UNTYPED route: in no document, no MCP tool, no op — and still declared,
	// because a route a host does not mount is a 405 on live traffic.
	app.Post("/v1/demo/ingest", func(c *zip.Ctx) error { return c.JSON(204, nil) })
	return app
}

// The declaration comes from the ROUTER, so it holds the untyped ingestion door
// too. That is the whole analytics-outage class: the published paths were
// listed, the four ingestion doors were not, and every beacon answered 405.
func TestDeclarationIsTheRouter(t *testing.T) {
	d := declApp(t).Declaration()
	if d.Name != "demo" {
		t.Fatalf("name = %q, want demo", d.Name)
	}
	want := []zip.Route{
		{Method: "POST", Pattern: "/v1/demo/ingest"},
		{Method: "POST", Pattern: "/v1/demo/quote"},
		{Method: "GET", Pattern: "/v1/demo/quotes/:id"},
	}
	if len(d.Routes) != len(want) {
		t.Fatalf("routes = %+v, want %+v", d.Routes, want)
	}
	for i := range want {
		if d.Routes[i] != want[i] {
			t.Fatalf("route %d = %+v, want %+v", i, d.Routes[i], want[i])
		}
	}
	// The op token is the operation's ONE identity, so only typed ops appear.
	if len(d.Ops) != 2 || d.Ops[0] != "demo_quote" || d.Ops[1] != "demo_quote_read" {
		t.Fatalf("ops = %v, want [demo_quote demo_quote_read]", d.Ops)
	}
	// zip's own control plane is the host's, per process, never a child's claim.
	for _, r := range d.Routes {
		if r.Pattern == zip.PluginPath || r.Pattern == "/.well-known/openapi.json" {
			t.Fatalf("declaration leaked zip's control plane: %v", r)
		}
	}
}

// Eager is the one fact the router cannot show, so it travels in the
// declaration and comes from the app's own Config.
func TestDeclarationCarriesEager(t *testing.T) {
	if declApp(t).Declaration().Eager {
		t.Fatal("a request-driven app declared itself eager")
	}
	app := zip.New(zip.Config{AppName: "pubsub", Eager: true, DisableStartupMessage: true})
	app.Get("/v1/pubsub/topics", func(c *zip.Ctx) error { return nil })
	if !app.Declaration().Eager {
		t.Fatal("an app that owns a consumer declared itself lazy")
	}
}

// Described writes a FILE, never stdout: a plugin's own dependencies log at
// construction and `> file` splices those into the front of the document.
func TestDescribedWritesBothProjections(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		verb string
		want string
	}{{"declare", "demo_quote"}, {"openapi", "/v1/demo/quote"}} {
		dest := filepath.Join(dir, "sub", tc.verb+".json")
		old := os.Args
		os.Args = []string{"demo", tc.verb, dest}
		done, err := declApp(t).Described()
		os.Args = old
		if !done || err != nil {
			t.Fatalf("%s: done=%v err=%v", tc.verb, done, err)
		}
		body, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("%s: %v", tc.verb, err)
		}
		var v map[string]any
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("%s: not JSON: %v", tc.verb, err)
		}
		if !containsStr(string(body), tc.want) {
			t.Fatalf("%s: %s missing from %s", tc.verb, tc.want, body)
		}
	}
}

// A main with no projection verb must fall through to Listen, and an unknown
// argv must not be swallowed as one.
func TestDescribedIgnoresEverythingElse(t *testing.T) {
	for _, args := range [][]string{{"demo"}, {"demo", "serve"}, {"demo", "--help"}} {
		old := os.Args
		os.Args = args
		done, err := declApp(t).Described()
		os.Args = old
		if done || err != nil {
			t.Fatalf("%v: done=%v err=%v — want a fall-through to Listen", args, done, err)
		}
	}
}

// A projection verb with no destination is a mistake worth failing on, not a
// reason to write to stdout.
func TestDescribedRefusesAMissingDestination(t *testing.T) {
	old := os.Args
	os.Args = []string{"demo", "declare"}
	done, err := declApp(t).Described()
	os.Args = old
	if !done || err == nil {
		t.Fatalf("done=%v err=%v — want a refusal", done, err)
	}
}

func containsStr(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
