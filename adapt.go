// Adapters for bringing existing net/http code onto zip
// without rewriting handlers. These are MIGRATION TOOLS — each adapter
// costs ~5% perf vs native Fiber dispatch and exists so a service can
// roll onto zip incrementally. Replace adapted routes with native
// zip handlers when feasible.

package zip

import (
	"net/http"

	"github.com/zap-proto/fiber/v3/middleware/adaptor"
)

// AdaptNetHTTP wraps an http.Handler so it can be served on a zip router as an
// ordinary zip.Handler. To front a whole foreign subtree, take a Group for the
// prefix and register a wildcard on it — this is THE way to bring net/http code
// onto zip:
//
//	app.Group("/legacy").All("/*", zip.AdaptNetHTTP(httpHandler))
//
// Group is what carries the prefix; Mount does not do this. Mount delegates a
// prefix to a REMOTE address over a transport, so it takes a string, not a
// handler. A local handler is composed, not delegated.
//
// The wildcard stays an ordinary route, so most-specific-wins precedence still
// applies: a route registered on the group afterwards still beats it for its
// exact path.
//
//	g := app.Group("/legacy")
//	g.All("/*", zip.AdaptNetHTTP(httpHandler)) // everything else
//	g.Get("/health", nativeHealth)             // still wins
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
// as a zip.Handler — the net/http-middleware bridge. Because it IS middleware,
// it goes on a Group's Use rather than on a route:
//
//	app.Group("/legacy").Use(zip.AdaptNetHTTPMiddleware(mw))
//
// Migration tool — costs ~5% perf vs native Fiber.
func AdaptNetHTTPMiddleware(mw func(http.Handler) http.Handler) Handler {
	wrapped := adaptor.HTTPMiddleware(mw)
	return func(c *Ctx) error { return wrapped(c.fc) }
}
