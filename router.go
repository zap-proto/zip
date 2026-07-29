package zip

import (
	"strings"

	"github.com/zap-proto/fiber/v3"
)

// OpTarget is a place a typed op can be declared: the [App], or any Router of
// it — a Group, or the result of With. [Get] and friends take one of these, so
// `zip.Get(app, …)` and `zip.Get(v1, …)` are the same declaration with the same
// meaning, and a group-structured app does not have to spell its prefix out per
// route to have typed ops.
//
// Its only method is unexported, which seals it: an op lives on an App's
// registry, and nothing outside this package can claim to be somewhere else it
// could live.
type OpTarget interface {
	// opTarget yields what registerTyped needs — the App that owns the op
	// registry, the path prefix this router sits under, and the middleware to
	// compose around the op's handler (nil for none).
	opTarget() (*App, string, Middleware)
}

// Router is the path-mounting surface shared by *App and Group.
// All concrete routes flow through toFiberHandler — fiber.Ctx is
// the underlying type the framework's users never see directly.
type Router interface {
	// Every Router is somewhere a typed op can be declared, so `zip.Get(v1, …)`
	// takes a Group as readily as it takes the App.
	OpTarget

	Use(handlers ...Handler) Router

	// Route registration takes ONE chain in wrapping order: zero or more
	// middleware first, the final handler LAST. fiber wants handler-first;
	// splitChain flips it in exactly one place.
	Get(path string, handlers ...Handler) Router
	Post(path string, handlers ...Handler) Router
	Put(path string, handlers ...Handler) Router
	Patch(path string, handlers ...Handler) Router
	Delete(path string, handlers ...Handler) Router
	Head(path string, handlers ...Handler) Router
	Options(path string, handlers ...Handler) Router
	All(path string, handlers ...Handler) Router

	Group(prefix string, handlers ...Handler) Router

	// Fiber returns the underlying fiber.Router for one-off escape.
	Fiber() fiber.Router
}

// routerAdapter wraps a fiber.Router (App or Group) and exposes the
// zip-style Handler signature.
type routerAdapter struct {
	r   fiber.Router
	app *App
}

func (a *routerAdapter) Use(handlers ...Handler) Router {
	for _, h := range handlers {
		a.r.Use(toFiberHandler(a.app, h))
	}
	return a
}

func (a *routerAdapter) Get(path string, handlers ...Handler) Router {
	h, mw := splitChain(a.app, handlers)
	a.r.Get(normPath(path), h, mw...)
	return a
}

func (a *routerAdapter) Post(path string, handlers ...Handler) Router {
	h, mw := splitChain(a.app, handlers)
	a.r.Post(normPath(path), h, mw...)
	return a
}

func (a *routerAdapter) Put(path string, handlers ...Handler) Router {
	h, mw := splitChain(a.app, handlers)
	a.r.Put(normPath(path), h, mw...)
	return a
}

func (a *routerAdapter) Patch(path string, handlers ...Handler) Router {
	h, mw := splitChain(a.app, handlers)
	a.r.Patch(normPath(path), h, mw...)
	return a
}

func (a *routerAdapter) Delete(path string, handlers ...Handler) Router {
	h, mw := splitChain(a.app, handlers)
	a.r.Delete(normPath(path), h, mw...)
	return a
}

func (a *routerAdapter) Head(path string, handlers ...Handler) Router {
	h, mw := splitChain(a.app, handlers)
	a.r.Head(normPath(path), h, mw...)
	return a
}

func (a *routerAdapter) Options(path string, handlers ...Handler) Router {
	h, mw := splitChain(a.app, handlers)
	a.r.Options(normPath(path), h, mw...)
	return a
}

func (a *routerAdapter) All(path string, handlers ...Handler) Router {
	h, mw := splitChain(a.app, handlers)
	a.r.All(normPath(path), h, mw...)
	return a
}

func (a *routerAdapter) Group(prefix string, handlers ...Handler) Router {
	args := make([]any, 0, len(handlers))
	for _, h := range handlers {
		args = append(args, toFiberHandler(a.app, h))
	}
	g := a.r.Group(prefix, args...)
	return &routerAdapter{r: g, app: a.app}
}

func (a *routerAdapter) Fiber() fiber.Router { return a.r }

// opTarget reads the prefix off the ROUTER rather than keeping a second copy of
// it: fiber composes a nested group's prefix when the group is made, so asking
// it is the one reading that cannot drift from where the routes actually land.
func (a *routerAdapter) opTarget() (*App, string, Middleware) {
	if g, ok := a.r.(*fiber.Group); ok {
		return a.app, g.Prefix, nil
	}
	return a.app, "", nil
}

// joinPath composes a group's prefix with a leaf path the way the router does,
// so a typed op's identity IS the route it registered — the document, the tool
// name and the command all read op.Path, and a path composed by a second rule
// would name a route that does not exist.
func joinPath(prefix, path string) string {
	path = normPath(path)
	if prefix == "" {
		return path
	}
	if path[0] != '/' {
		path = "/" + path
	}
	return strings.TrimRight(prefix, "/") + path
}

// splitChain adapts one registration chain — middleware first, the final
// handler LAST — to fiber's variadic signature. fiber
// executes route handlers in ARGUMENT order (the first argument enters first
// and Next() descends), so the chain passes through verbatim: first element,
// then the rest. Registering a route with no handler is a programmer error
// and panics at boot, never at request time.
// normPath maps the empty leaf to the group root: Get("") on a Group("/x")
// means "/x". fiber never matches an empty path, so
// the normalization lives here — one place, every route method.
func normPath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

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
