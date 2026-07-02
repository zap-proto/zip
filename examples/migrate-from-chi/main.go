// migrate-from-chi example — chi → zip via stdlib adapter.
//
// Use zip.AdaptNetHTTP(chiRouter) to mount an existing chi.Router as a
// migration step. Replace adapted routes with native zip handlers when
// feasible; the adapter costs ~5% perf vs native Fiber dispatch.
//
// chi is NOT a dep of this example — we adapt any http.Handler, and a
// chi.Router satisfies that interface natively.
package main

import (
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

func main() {
	app := zip.New(zip.Config{AppName: "migrate-from-chi"})
	app.Use(middleware.Recover())

	// Native zip routes for new work.
	app.Get("/v1/users/:id", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"id": c.Param("id")})
	})

	// Mount the legacy chi router under /legacy/chi for incremental
	// migration. Replace one path at a time with native zip handlers.
	app.Mount("/legacy/chi", legacyHandler{})

	log.Fatal(app.ListenHTTP(":8080"))
}
