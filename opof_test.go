package zip_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/zap-proto/zip"
)

type tailIn struct {
	Follow bool `json:"follow"`
}

// where is the address the handler was asked at.
type where struct {
	Address string `json:"address"`
	ID      string `json:"id"`
}

// tail is the shape a service takes when it forwards what it was asked for: a
// BOUND METHOD registered directly, reading its own address from the context.
type tail struct{}

// Livetail streams matching records as they arrive.
func (tail) Livetail(ctx context.Context, _ *tailIn) (*where, error) {
	op, ok := zip.OpOf(ctx)
	if !ok {
		return &where{Address: "", ID: "no operation"}, nil
	}
	return &where{Address: op.Path, ID: op.OperationID}, nil
}

func addressAt(t *testing.T, app *zip.App, path string) where {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", path, nil))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: status %d", path, resp.StatusCode)
	}
	var w where
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return w
}

// A HANDLER IS TOLD THE ADDRESS IT WAS ASKED AT, and the registration stays the
// bound method — no closure, no factory, nothing between the source and the
// projections that read it.
//
// This is what a registration-time wrapper was for: the address was composed by
// hand and threaded in, which made every such op invisible to cmd/zipdoc,
// because a registration made inside a helper is not a registration this pass
// can see. Reading it from the context leaves the declaration direct.
func TestOpOf_IsTheAddressTheHandlerWasAskedAt(t *testing.T) {
	app := zip.New(zip.Config{AppName: "o11y", DisableStartupMessage: true})
	app.Group("/v1/o11y").Get("/logs/livetail", tail{}.Livetail)

	if got := addressAt(t, app, "/v1/o11y/logs/livetail").Address; got != "/v1/o11y/logs/livetail" {
		t.Errorf("address = %q, want the composed path", got)
	}

	// And the op is in the registry under that address, so what the handler reads
	// and what the document publishes are the same string.
	var addrs []string
	for _, op := range app.Manifest().Ops {
		addrs = append(addrs, op.Method+" "+op.Path)
	}
	sort.Strings(addrs)
	if len(addrs) != 1 || addrs[0] != "GET /v1/o11y/logs/livetail" {
		t.Errorf("registry = %v, want the one op at the address the handler reads", addrs)
	}
}

// ONE HANDLER, TWO ADDRESSES. A definition composed under two hosts answers at
// both, and each call is told where IT was asked — which is the whole reason
// this is a property of the call and not something a group could hand out when
// it was declared.
func TestOpOf_ADefinitionComposedTwiceAnswersAtBoth(t *testing.T) {
	child := zip.New(zip.Config{AppName: "tail", DisableStartupMessage: true})
	child.Get("/livetail", tail{}.Livetail)

	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	host.Group("/east").Use(child)
	host.Group("/west").Use(child)
	if err := host.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got := addressAt(t, host, "/east/livetail").Address; got != "/east/livetail" {
		t.Errorf("east address = %q", got)
	}
	if got := addressAt(t, host, "/west/livetail").Address; got != "/west/livetail" {
		t.Errorf("west address = %q — one handler, and each call is told its own", got)
	}
}

// Called directly rather than through the declaration that registered it, there
// is no operation, and saying so beats guessing an address.
func TestOpOf_SaysWhenThereIsNoOperation(t *testing.T) {
	w, err := tail{}.Livetail(context.Background(), &tailIn{})
	if err != nil {
		t.Fatal(err)
	}
	if w.Address != "" {
		t.Errorf("address = %q, want empty outside a registration", w.Address)
	}
}
