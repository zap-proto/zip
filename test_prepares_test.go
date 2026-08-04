package zip

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type doorIn struct{}
type doorOut struct{ OK bool }

func doorHandler(context.Context, *doorIn) (*doorOut, error) { return &doorOut{true}, nil }

// TestTestSeesTheSameSurfaceServingWould is the invariant [App.Test] claims and
// did not hold: "it builds the program if nothing is live, exactly as serving
// would".
//
// Serving calls prepare, which installs the deferred projections — /mcp, the
// OpenAPI document, the op-call plane, the plugin route. Building does not. So
// Test used to answer 404 on all four while production answered 200: a test could
// not see the surface it was written to check, and the closer it got to the real
// program the more confidently it reported the wrong thing.
//
// Callers papered over that with an exported Prepare they had to remember to
// call, which is why unexporting it broke hanzoai/cloud's MCP tests rather than
// fixing them. The projections belong to "run the program", not to the caller's
// discipline.
func TestTestSeesTheSameSurfaceServingWould(t *testing.T) {
	app := New(Config{AppName: "door", DisableStartupMessage: true})
	Post(app, "/thing", doorHandler)

	req, _ := http.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("/mcp answered 404 under Test — the door serving installs is missing, " +
			"so a test cannot see the surface production exposes")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/mcp = %d, want 200", resp.StatusCode)
	}
}

// TestTestPreparesIdempotently: prepare runs at most once, so repeated Test calls
// (the normal shape of a table test) must not reinstall the projections and
// collide on their own routes.
func TestTestPreparesIdempotently(t *testing.T) {
	app := New(Config{AppName: "door2", DisableStartupMessage: true})
	Post(app, "/thing", doorHandler)

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Test %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Test %d: /mcp = %d, want 200", i, resp.StatusCode)
		}
	}
}
