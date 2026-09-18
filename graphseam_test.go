package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

type probeIn struct{}
type probeOut struct {
	OK bool `json:"ok"`
}

type crm struct{}

// Probe answers whether the surface is up.
func (crm) Probe(context.Context, *probeIn) (*probeOut, error) { return &probeOut{OK: true}, nil }

// EVERY SEAM STATES THE ADDRESS, not just the REST route.
//
// An op reached by NAME — through the graph, a tools/call, the call plane or an
// in-process invoke — is the same operation as the one reached by path, so the
// rule has to see the same identity either way. The graph resolved ops out of
// the composed registry and then invoked them without saying so, which left the
// contract falling back to the declaration: for anything declared on a group,
// the bare leaf. A rule reading op.Path governed the REST call and not the graph
// call to the same operation.
func TestGraph_ARuleSeesTheServedAddress(t *testing.T) {
	app := zip.New(zip.Config{AppName: "crm", DisableStartupMessage: true})
	app.Group("").Group("/v1/crm").Post("/probe", crm{}.Probe, zip.WithOperationID("crm_probe"))
	app.MountGraph("/v1/graphql")

	var seen []string
	app.Authorize(func(_ context.Context, op zip.Op, _ any) (zip.Decision, error) {
		seen = append(seen, op.Method+" "+op.Path+" "+op.OperationID)
		return zip.Decision{Effect: zip.Allow}, nil
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}

	// Through the graph, by name.
	body, _ := json.Marshal(zip.GraphRequest{Query: `mutation { crm_probe { ok } }`})
	req := httptest.NewRequest(http.MethodPost, "/v1/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// And over REST, by path.
	rr := httptest.NewRequest(http.MethodPost, "/v1/crm/probe", bytes.NewReader([]byte(`{}`)))
	rr.Header.Set("Content-Type", "application/json")
	resp2, err := app.Test(rr)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()

	if len(seen) != 2 {
		t.Fatalf("authorizer ran %d times, want 2 (graph and REST): %v", len(seen), seen)
	}
	const want = "POST /v1/crm/probe crm_probe"
	for i, got := range seen {
		if got != want {
			t.Errorf("call %d saw %q, want %q — both seams answer for one operation", i, got, want)
		}
	}
}
