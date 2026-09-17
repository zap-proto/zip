package zip

import (
	"github.com/zap-proto/zip/internal/addr"

	"github.com/zap-proto/fiber/v3"
)

// opScope is where an op declared on a group lands: the App whose registry
// holds it, the path prefix its route sits under, and the middleware composed
// around its handler.
type opScope struct {
	// App owns the op registry. Every op ends up on exactly one.
	App *App

	// Prefix is prepended to the op's path, as a Group's prefix is prepended to
	// an ordinary route's.
	Prefix string

	// Middleware wraps the op's handler. nil means none — the common case, and
	// the one that costs nothing.
	Middleware Middleware
}

// joinPath composes a group's prefix with a leaf path the way the router does,
// so a typed op's identity IS the route it registered — the document, the tool
// name and the command all read op.Path, and a path composed by a second rule
// would name a route that does not exist.
func joinPath(prefix, path string) string { return addr.Join(prefix, path) }

// splitChain adapts one registration chain — middleware first, the final
// handler LAST — to fiber's variadic signature. fiber
// executes route handlers in ARGUMENT order (the first argument enters first
// and Next() descends), so the chain passes through verbatim: first element,
// then the rest. Registering a route with no handler is a programmer error
// and panics at boot, never at request time.
// normPath maps the empty leaf to the group root: Get("") on a Group("/x")
// means "/x". fiber never matches an empty path, so
// the normalization lives here — one place, every route method.
// normPath and joinPath are [addr.Norm] and [addr.Join]. The rule lives in one
// package because cmd/zipdoc composes the same two things to find a route's
// prose, and a second copy of it drifted.
func normPath(path string) string { return addr.Norm(path) }

func splitChain(app *App, handlers []Handler) (fiber.Handler, []any) {
	if len(handlers) == 0 {
		panic("zip: route registered with no handler")
	}
	first := toFiberHandler(app, handlers[0])
	rest := make([]any, 0, len(handlers)-1)
	for _, h := range handlers[1:] {
		rest = append(rest, toFiberHandler(app, h))
	}
	return first, rest
}

// toFiberHandler turns a zip.Handler into a fiber.Handler, materialising
// the per-request *Ctx and forwarding errors to fiber's error chain (which
// runs through zip's default errorHandler).
func toFiberHandler(app *App, h Handler) fiber.Handler {
	return func(fc fiber.Ctx) error {
		return h(requestCtx(app, fc))
	}
}

// ctxKey names the request-scoped slot holding this request's one *Ctx. A
// zero-size unexported type: unforgeable by other packages, and boxing it
// into the `any` key allocates nothing.
type ctxKey struct{}

// requestCtx returns THE *Ctx for this request, creating it on first touch.
//
// One request, one Ctx. Every zip handler the request passes through — Use
// middleware, group middleware, the leaf — is handed the same value, so
// c.SetLog() in middleware reaches the handlers after it (that is what
// middleware.Logger has always meant to do), and the wrapper costs one
// allocation per REQUEST rather than one per handler in the chain.
//
// The slot is the request's own user-value storage, so the lifetime is
// exactly the request's: fasthttp clears user values when it resets the
// request, before the connection serves the next one. Ctx therefore has
// fiber's lifetime rule, not a longer one — do not retain it past the
// handler.
func requestCtx(app *App, fc fiber.Ctx) *Ctx {
	rc := fc.RequestCtx()
	if c, ok := rc.UserValue(ctxKey{}).(*Ctx); ok && c.app == app {
		c.fc = fc // same request; bind to the ctx actually driving this call
		return c
	}
	c := &Ctx{fc: fc, app: app, log: app.logger}
	rc.SetUserValue(ctxKey{}, c)
	return c
}
