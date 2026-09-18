package zip_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

type traceIn struct {
	ID string `json:"id"`
}

type seen struct {
	Pattern string `json:"pattern"`
	Address string `json:"address"`
	HasAddr bool   `json:"hasAddr"`
}

type tracer struct{}

// Spans returns the spans of one trace.
func (tracer) Spans(ctx context.Context, _ *traceIn) (*seen, error) {
	op, _ := zip.OpOf(ctx)
	addr, ok := zip.AddressOf(ctx)
	return &seen{Pattern: op.Path, Address: addr, HasAddr: ok}, nil
}

// A RELAY NEEDS BOTH: the pattern says which operation, the address says which
// resource. A handler that forwards has to name both, and before this it could
// only get the second by being wrapped in a closure that held the decoded input
// — which is what made a whole service's registrations invisible to cmd/zipdoc.
func TestAddressOf_IsThisCallsConcreteAddress(t *testing.T) {
	app := zip.New(zip.Config{AppName: "traces", DisableStartupMessage: true})
	app.Group("/v1/o11y").Get("/traces/:id", tracer{}.Spans)

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/o11y/traces/abc123", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got seen
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if want := "/v1/o11y/traces/:id"; got.Pattern != want {
		t.Errorf("pattern = %q, want %q", got.Pattern, want)
	}
	if !got.HasAddr {
		t.Fatal("no address; a handler inside its registration always has one")
	}
	if want := "/v1/o11y/traces/abc123"; got.Address != want {
		t.Errorf("address = %q, want %q — the parameter filled from the input", got.Address, want)
	}
}

// THE CONCRETE ADDRESS IS NOT ON Op, deliberately — but the PATTERN on it is
// the one the call was served at.
//
// Op is what the authorizer is handed and what OnResult is told. It carries the
// composed pattern and the composed operation id: the same identity the route
// table and the by-name registry publish. It does NOT carry the concrete
// address, because path parameters are input and OnResult is promised the
// operation and the outcome and never the input.
//
// Both halves were wrong the other way once. A rule keyed on op.Path saw the
// bare leaf for anything declared on a group — "/traces/:id" for an op served
// at "/v1/o11y/traces/:id" — so every path-keyed rule in a service that
// declares relatively governed an address nobody serves, and one definition
// mounted at two prefixes reported one name for two operations.
func TestOp_ARuleIsHandedTheOpAsServed(t *testing.T) {
	app := zip.New(zip.Config{AppName: "traces", DisableStartupMessage: true})
	app.Group("/v1/o11y").Get("/traces/:id", tracer{}.Spans)

	var asked []string
	var told string
	app.Authorize(func(_ context.Context, op zip.Op, _ any) (zip.Decision, error) {
		asked = append(asked, op.Method+" "+op.Path+" id="+op.OperationID)
		return zip.Decision{Effect: zip.Allow}, nil
	})
	app.OnResult(func(_ context.Context, op zip.Op, _ error) { told = op.Path + " id=" + op.OperationID })
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/o11y/traces/abc123", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// The composed pattern and the id derived from it, exactly as the route
	// table and the registry carry them.
	want := "GET /v1/o11y/traces/:id id=" + zip.ID("GET", "/v1/o11y/traces/:id")
	if len(asked) != 1 || asked[0] != want {
		t.Errorf("authorizer saw %v, want [%q]", asked, want)
	}
	if told != "/v1/o11y/traces/:id id="+zip.ID("GET", "/v1/o11y/traces/:id") {
		t.Errorf("result hook was told %q", told)
	}

	// And never the resolved address: parameters are input.
	for _, s := range append(asked, told) {
		if strings.Contains(s, "abc123") {
			t.Errorf("a rule was handed the concrete address in %q; path parameters are input", s)
		}
	}
}

// A DECLARED id is the operation's own name, and composition does not get a
// vote on it — the same rule occurrenceID applies in the registry.
func TestOp_ADeclaredIDSurvivesComposition(t *testing.T) {
	app := zip.New(zip.Config{AppName: "traces", DisableStartupMessage: true})
	app.Group("/v1/o11y").Get("/traces/:id", tracer{}.Spans).ID("traces.spans")

	var seen string
	app.Authorize(func(_ context.Context, op zip.Op, _ any) (zip.Decision, error) {
		seen = op.OperationID + " at " + op.Path
		return zip.Decision{Effect: zip.Allow}, nil
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(httptest.NewRequest("GET", "/v1/o11y/traces/abc", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if seen != "traces.spans at /v1/o11y/traces/:id" {
		t.Errorf("rule saw %q, want the declared id at the composed path", seen)
	}
}

// An op with no parameter has an address equal to its pattern, and pays no
// reflection to find that out.
func TestAddressOf_NoParameterIsThePatternItself(t *testing.T) {
	app := zip.New(zip.Config{AppName: "traces", DisableStartupMessage: true})
	app.Group("/v1/o11y").Get("/stats", tracer{}.Spans)

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/o11y/stats", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got seen
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Address != "/v1/o11y/stats" || got.Pattern != got.Address {
		t.Errorf("address = %q, pattern = %q; both are the same with no parameter", got.Address, got.Pattern)
	}
}
