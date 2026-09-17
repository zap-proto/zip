package zip_test

import (
	"context"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/zap-proto/zip"
)

type rootIn struct{}
type rootOut struct {
	OK bool `json:"ok"`
}

type coll struct{}

// List returns the collection.
func (coll) List(context.Context, *rootIn) (*rootOut, error) { return &rootOut{OK: true}, nil }

// Add adds to it.
func (coll) Add(context.Context, *rootIn) (*rootOut, error) { return &rootOut{OK: true}, nil }

// AN EMPTY LEAF IS THE PREFIX ITSELF. `g.Get("", h)` declares the collection
// root at the group's own address; only an explicit "/" asks for the trailing
// slash.
//
// Serving was never the problem — both spellings answer either way. The
// DECLARED pattern is what the document, the op key and cmd/zipdoc publish, and
// a group that could not spell its own root is why a service declared that route
// at an absolute path on the subsystem root instead, which put it outside the
// middleware of the group it belonged to.
func TestGroup_AnEmptyLeafIsTheCollectionRoot(t *testing.T) {
	app := zip.New(zip.Config{AppName: "coll", DisableStartupMessage: true})
	g := app.Group("/v1/function")
	g.Get("", coll{}.List)
	g.Post("/", coll{}.Add)

	var declared []string
	for _, r := range app.Routes() {
		declared = append(declared, r.Method+" "+r.Pattern)
	}
	sort.Strings(declared)

	// Both spellings canonicalise to the same address; they differ only by verb.
	want := []string{"GET /v1/function", "POST /v1/function"}
	if len(declared) != 2 || declared[0] != want[0] || declared[1] != want[1] {
		t.Errorf("declared %v, want %v", declared, want)
	}

	// And it still answers, at the address it now declares.
	for _, addr := range []string{"/v1/function", "/v1/function/"} {
		resp, err := app.Test(httptest.NewRequest("GET", addr, nil))
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d, want 200", addr, resp.StatusCode)
		}
	}
}

// The root app is unaffected: with no prefix there is nothing for an empty leaf
// to be, so it stays the root.
func TestGroup_AnEmptyLeafAtTheRootIsStillTheRoot(t *testing.T) {
	app := zip.New(zip.Config{AppName: "coll", DisableStartupMessage: true})
	app.Get("", coll{}.List)
	for _, r := range app.Routes() {
		if r.Pattern != "/" {
			t.Errorf("declared %q, want /", r.Pattern)
		}
	}
}

type ingestIn struct {
	Project string `json:"project"`
}

type ingest struct{}

// Envelope accepts a Sentry envelope for one project.
func (ingest) Envelope(ctx context.Context, _ *ingestIn) (*rootOut, error) {
	seenPattern, _ := zip.OpOf(ctx)
	seenAddr, _ := zip.AddressOf(ctx)
	ingestPattern, ingestAddr = seenPattern.Path, seenAddr
	return &rootOut{OK: true}, nil
}

var ingestPattern, ingestAddr string

// EVERY READER OF AN ADDRESS AGREES ON HOW IT IS SPELLED.
//
// A pattern written with a trailing slash — which the Sentry-compatible ingest
// addresses are, deliberately — is canonicalised once, so the route table, the
// document, the operation a handler reads and the concrete address it forwards
// to are all the same string. A service that resolves its runtime handler BY
// NAME from Template(pattern) is what makes this load-bearing: if registration
// trimmed and the lookup did not, every ingest call would refuse.
func TestGroup_EveryReaderSpellsTheAddressTheSameWay(t *testing.T) {
	app := zip.New(zip.Config{AppName: "ingest", DisableStartupMessage: true})
	app.Group("/v1/event/:project").Post("/envelope/", ingest{}.Envelope)

	const want = "/v1/event/:project/envelope"

	routes := app.Routes()
	if len(routes) != 1 || routes[0].Pattern != want {
		t.Fatalf("Routes() = %+v, want one at %q", routes, want)
	}

	ops := app.Manifest().Ops
	if len(ops) != 1 || ops[0].Path != want {
		t.Fatalf("registry = %+v, want one at %q", ops, want)
	}

	if got := zip.Template(want); got != "/v1/event/{project}/envelope" {
		t.Errorf("Template = %q; the document's spelling of the same address", got)
	}

	// And the request serves under BOTH spellings, reporting one.
	for _, addr := range []string{"/v1/event/abc/envelope", "/v1/event/abc/envelope/"} {
		ingestPattern, ingestAddr = "", ""
		resp, err := app.Test(httptest.NewRequest("POST", addr, nil))
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("POST %s = %d, want 200", addr, resp.StatusCode)
			continue
		}
		if ingestPattern != want {
			t.Errorf("POST %s: OpOf reported %q, want %q", addr, ingestPattern, want)
		}
		if ingestAddr != "/v1/event/abc/envelope" {
			t.Errorf("POST %s: AddressOf reported %q", addr, ingestAddr)
		}
	}
}
