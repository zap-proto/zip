package zip_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
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

// THE CONCRETE ADDRESS IS NOT ON Op, deliberately.
//
// Op is what the authorizer is handed and what OnResult is told, and both are
// narrow on purpose — the result hook is given the operation and the outcome and
// never the input. A concrete address carries path parameters, which ARE input,
// so putting it on Op would widen two contracts as a side effect of serving a
// third. This is the test that says so, because a later reader would otherwise
// fix the "inconsistency" by helpfully resolving Op.Path.
//
// It also pins what those two are handed TODAY, which is worth knowing: the
// DECLARED leaf, not the address the call is served at. So Op.Path means the
// declaration to a rule and the served pattern to [zip.OpOf], and a rule cannot
// tell two mountings of one definition apart. That is a real limit and it is
// older than this test; changing what authorization sees is its own decision,
// not a side effect of giving a handler its address.
func TestAddressOf_WhatARuleIsHandedIsUnchanged(t *testing.T) {
	app := zip.New(zip.Config{AppName: "traces", DisableStartupMessage: true})
	app.Group("/v1/o11y").Get("/traces/:id", tracer{}.Spans)

	var asked []string
	var toldPath string
	app.Authorize(func(_ context.Context, op zip.Op, _ any) (zip.Decision, error) {
		asked = append(asked, op.Path)
		return zip.Decision{Effect: zip.Allow}, nil
	})
	app.OnResult(func(_ context.Context, op zip.Op, _ error) { toldPath = op.Path })
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/o11y/traces/abc123", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// The declaration, unresolved — and no concrete address anywhere near it.
	if len(asked) != 1 || asked[0] != "/traces/:id" {
		t.Errorf("authorizer saw %v, want the declared leaf", asked)
	}
	if toldPath != "/traces/:id" {
		t.Errorf("result hook was told %q, want the declared leaf", toldPath)
	}
	for _, p := range append(asked, toldPath) {
		if p == "/v1/o11y/traces/abc123" {
			t.Errorf("a rule was handed the concrete address %q; path parameters are input", p)
		}
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
