package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

type guardIn struct {
	Org string `json:"org"`
}
type guardOut struct {
	OK bool `json:"ok"`
}

// A GROUP'S RULE COVERS ITS OWN OPS AND NO SIBLING'S.
//
// Both halves are load-bearing, and each one missing is its own defect. Without
// the rule on the guarded group, every write there runs authenticated but
// unchecked — a valid bearer editing another org's records. With the rule on the
// HOST instead, it becomes the host's rule, and [zip.App.Authorize] covers every
// op the host serves, so a sibling subsystem's ops get judged by rules written
// about a principal this guard never attached: a refusal on every valid call.
//
// hanzoai/iam is the service this is about. It used to reach a group's rule by
// asserting app.Group("") back to the concrete type, which the router no longer
// allows, so the rule needs a name of its own rather than a cast.
func TestGroup_AuthorizeCoversItsOwnOpsOnly(t *testing.T) {
	app := zip.New(zip.Config{AppName: "guarded", DisableStartupMessage: true})

	// The guarded subsystem: only its own org may write.
	guarded := app.Group("/v1/iam").Authorize(
		func(_ context.Context, _ zip.Op, in any) (zip.Decision, error) {
			gi, ok := in.(*guardIn)
			if !ok {
				t.Fatalf("authorizer got %T, want the decoded *guardIn", in)
			}
			if gi.Org != "mine" {
				return zip.Decision{Effect: zip.Deny, Clause: "org", Reason: "not your org"}, nil
			}
			return zip.Decision{Effect: zip.Allow}, nil
		})
	guarded.Post("/records", func(_ context.Context, _ *guardIn) (*guardOut, error) {
		return &guardOut{OK: true}, nil
	})

	// A sibling that declares no rule of its own, and must not inherit this one.
	app.Group("/v1/reports").Post("/run", func(_ context.Context, _ *guardIn) (*guardOut, error) {
		return &guardOut{OK: true}, nil
	})

	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	post := func(path, org string) int {
		b, _ := json.Marshal(guardIn{Org: org})
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("/v1/iam/records", "mine"); code != 200 {
		t.Errorf("guarded write by its own org = %d, want 200", code)
	}
	if code := post("/v1/iam/records", "someone-else"); code != 403 {
		t.Errorf("guarded write by another org = %d, want 403 — the group's rule is what checks it", code)
	}
	if code := post("/v1/reports/run", "someone-else"); code != 200 {
		t.Errorf("sibling write = %d, want 200 — a group's rule is not the host's", code)
	}
}
