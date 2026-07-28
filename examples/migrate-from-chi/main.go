// migrate-from-chi example — chi → zip via stdlib adapter.
//
// Use zip.AdaptNetHTTP(chiRouter) to mount an existing chi.Router as a
// migration step. Replace adapted routes with native zip handlers when
// feasible; the adapter costs ~5% perf vs native Fiber dispatch.
//
// chi is NOT a dep of this example — we adapt any http.Handler, and a
// chi.Router satisfies that interface natively.
//
// The adapted subtree is a wildcard route and registers no operation, so nothing
// behind /legacy/chi is in the OpenAPI document, the MCP tool list, the CLI or
// the call plane. That is the price of not rewriting it yet, and it is the
// reason to: new work goes in as a TYPED op, and a native route added later
// still wins by specificity, so a legacy path can be replaced one op at a time
// with no un-mount step.
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// legacyHandler stands in for an existing chi.Router. Same shape
// (http.Handler) so zip.AdaptNetHTTP works without changes.
type legacyHandler struct{}

func (legacyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"legacy":true}`))
}

// GetUserIn addresses one user; `id` is the path segment.
type GetUserIn struct {
	ID string `json:"id"`
}

// User is one user record.
type User struct {
	ID string `json:"id"`
}

// GetUser returns one user by id.
func getUser(_ context.Context, in *GetUserIn) (*User, error) {
	return &User{ID: in.ID}, nil
}

func main() {
	app := zip.New(zip.Config{AppName: "migrate-from-chi"})
	app.Use(middleware.Recover())

	// New work goes in typed: one declaration, and the route, the document, the
	// tool list, the CLI and the call plane all follow from it.
	zip.Get(app, "/v1/users/:id", getUser)

	// Front the legacy chi router under /legacy/chi for incremental
	// migration — one adapted wildcard route. Replace one path at a time
	// with native zip handlers; a native route added later wins by
	// specificity, no un-mount step needed.
	app.Group("/legacy/chi").All("/*", zip.AdaptNetHTTP(legacyHandler{}))

	log.Fatal(app.Listen("http://:8080"))
}
