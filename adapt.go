// Adapters for migrating existing chi / gin / net-http code onto zip
// without rewriting handlers. These are MIGRATION TOOLS — each adapter
// costs ~5% perf vs native Fiber dispatch and exists so a service can
// roll onto zip incrementally. Replace adapted routes with native
// zip handlers when feasible.

package zip

import (
	"net/http"

	"github.com/zap-proto/fiber/v3/middleware/adaptor"
)

// AdaptNetHTTP wraps an http.Handler so it can be served on a zip router
// as an ordinary zip.Handler. To front a whole foreign subtree, register
// it on a wildcard route — this is THE way to mount stdlib / chi / gin
// code:
//
//	app.All("/legacy/net/*", zip.AdaptNetHTTP(httpHandler))
//
// Migration tool — costs ~5% perf vs native Fiber. Replace with native
// zip handlers when feasible.
func AdaptNetHTTP(h http.Handler) Handler {
	wrapped := adaptor.HTTPHandler(h)
	return func(c *Ctx) error { return wrapped(c.fc) }
}

// AdaptNetHTTPFunc wraps an http.HandlerFunc.
//
// Migration tool — costs ~5% perf vs native Fiber.
func AdaptNetHTTPFunc(h http.HandlerFunc) Handler {
	wrapped := adaptor.HTTPHandlerFunc(h)
	return func(c *Ctx) error { return wrapped(c.fc) }
}

// AdaptNetHTTPMiddleware wraps a stdlib middleware (func(http.Handler) http.Handler).
//
// Migration tool — costs ~5% perf vs native Fiber.
func AdaptNetHTTPMiddleware(mw func(http.Handler) http.Handler) Handler {
	wrapped := adaptor.HTTPMiddleware(mw)
	return func(c *Ctx) error { return wrapped(c.fc) }
}

// Mount serves an http.Handler over the subtree under prefix.
//
// Deprecated: use app.All(prefix+"/*", zip.AdaptNetHTTP(h)) — the two
// primitives Mount is built from. Mount IS exactly that composition, kept as
// a behaviour-identical alias so existing callers keep working; preferring
// the explicit form leaves ONE way to put a route on the app. It also makes
// the mounted subtree's nature plain: it is an ordinary wildcard route, so a
// more specific static route wins even when registered later —
// app.Get(prefix+"/health", …) beats the mount by specificity, not order.
//
// chi.Router, gin.Engine, and a beego HandlerWrapper are all http.Handlers,
// so they mount the same way:
//
//	app.All("/legacy/chi/*", zip.AdaptNetHTTP(chiRouter))
//	app.All("/legacy/gin/*", zip.AdaptNetHTTP(ginEngine))
//	app.All("/legacy/iam/*", zip.AdaptNetHTTP(beegoApp.HandlerWrapper()))
//
// Migration tool — costs ~5% perf vs native Fiber.
func (a *App) Mount(prefix string, h http.Handler) {
	a.All(prefix+"/*", AdaptNetHTTP(h))
}
