package zip_test

// OnResult is told about every way an op can be reached, including the one no
// middleware can wrap.
//
// Everything a transport middleware records — a trail, a metric, a span — is
// recorded about a REQUEST. An in-process invoke is not one: it has no request,
// so nothing wraps it, and an operation reached that way leaves no trace of
// having run. That is the gap this hook exists for, and it is the row below that
// no other mechanism in the package can produce.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

type resIn struct {
	Fail bool `json:"fail"`
}

type resOut struct {
	OK bool `json:"ok"`
}

// told is one thing the hook was told.
type told struct {
	op  string
	err error
}

func resultApp(t *testing.T, seen *[]told) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "restest", DisableStartupMessage: true})
	zip.Post(app, "/v1/things", func(_ context.Context, in *resIn) (*resOut, error) {
		if in.Fail {
			return nil, errors.New("the handler said no")
		}
		return &resOut{OK: true}, nil
	}, zip.WithOperationID("createThing"))
	app.OnResult(func(_ context.Context, op zip.Op, err error) {
		*seen = append(*seen, told{op: op.OperationID, err: err})
	})
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

func TestOnResult_IsToldAboutEveryProjection(t *testing.T) {
	var seen []told
	app := resultApp(t, &seen)

	post := func(path string, body any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
	}

	post("/v1/things", map[string]any{"fail": false})
	post("/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "createThing", "arguments": map[string]any{"fail": false}},
	})
	// The in-process seam: no request, no middleware, and until this hook no
	// record that it happened at all.
	if _, err := zip.Here[resIn, resOut](context.Background(), app, "createThing", &resIn{}); err != nil {
		t.Fatalf("in-process invoke: %v", err)
	}

	if len(seen) != 3 {
		t.Fatalf("the hook was told %d times, want 3 (REST, MCP, in-process): %+v", len(seen), seen)
	}
	for i, s := range seen {
		if s.op != "createThing" {
			t.Errorf("told %d names operation %q, want createThing", i, s.op)
		}
		if s.err != nil {
			t.Errorf("told %d carries err %v, want nil", i, s.err)
		}
	}
}

// A FAILURE IS AN OUTCOME. A hook told only about successes would record a
// surface that never fails, which is the opposite of what a trail is for.
func TestOnResult_IsToldWhenTheHandlerFails(t *testing.T) {
	var seen []told
	app := resultApp(t, &seen)

	b, _ := json.Marshal(map[string]any{"fail": true})
	req := httptest.NewRequest("POST", "/v1/things", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("REST: %v", err)
	}
	_ = resp.Body.Close()

	if len(seen) != 1 {
		t.Fatalf("the hook was told %d times, want 1", len(seen))
	}
	if seen[0].err == nil {
		t.Error("the hook was told nil for a handler that returned an error")
	}
}

// A REFUSAL IS AN OUTCOME TOO, and the one a trail most needs: a caller that was
// stopped is more interesting than one that was not.
func TestOnResult_IsToldWhenTheRuleRefuses(t *testing.T) {
	var seen []told
	app := zip.New(zip.Config{AppName: "restest2", DisableStartupMessage: true})
	var ran bool
	zip.Post(app, "/v1/things", func(_ context.Context, in *resIn) (*resOut, error) {
		ran = true
		return &resOut{OK: true}, nil
	}, zip.WithOperationID("createThing"))
	app.Authorize(func(_ context.Context, _ zip.Op, _ any) (zip.Decision, error) {
		return zip.Decision{Effect: zip.Deny, Clause: "none", Reason: "no"}, nil
	})
	app.OnResult(func(_ context.Context, op zip.Op, err error) {
		seen = append(seen, told{op: op.OperationID, err: err})
	})
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	b, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest("POST", "/v1/things", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("REST: %v", err)
	}
	_ = resp.Body.Close()

	if ran {
		t.Fatal("the handler ran despite a Deny")
	}
	if len(seen) != 1 || seen[0].err == nil {
		t.Fatalf("a refused op told the hook %+v, want one entry carrying the refusal", seen)
	}
}

// NO HOOK, NO COST, NO CHANGE. The default is unchanged for every app that
// declares none.
func TestOnResult_NilIsSilent(t *testing.T) {
	app := zip.New(zip.Config{AppName: "restest3", DisableStartupMessage: true})
	zip.Post(app, "/v1/things", func(_ context.Context, in *resIn) (*resOut, error) {
		return &resOut{OK: true}, nil
	}, zip.WithOperationID("createThing"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest("POST", "/v1/things", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("REST: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
