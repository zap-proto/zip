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

// AdaptNetHTTPMiddleware wraps a stdlib middleware (func(http.Handler) http.Handler)
// as a zip.Handler. Register it on a wildcard route to front a foreign subtree
// whose entry point is a net/http middleware (e.g. a gin engine bridged via
// NoRoute) — this is THE net/http-middleware bridge:
//
//	app.All("/legacy/*", zip.AdaptNetHTTPMiddleware(mw))
//
// Migration tool — costs ~5% perf vs native Fiber.
func AdaptNetHTTPMiddleware(mw func(http.Handler) http.Handler) Handler {
	wrapped := adaptor.HTTPMiddleware(mw)
	return func(c *Ctx) error { return wrapped(c.fc) }
}
