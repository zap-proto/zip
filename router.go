package zip

import (
	"strings"

	"github.com/zap-proto/fiber/v3"
)

// OpScope is where an op declared on a router lands: the App whose registry
// holds it, the path prefix its route sits under, and the middleware composed
// around its handler.
type OpScope struct {
	// App owns the op registry. Every op ends up on exactly one.
	App *App

	// Prefix is prepended to the op's path, as a Group's prefix is prepended to
	// an ordinary route's.
	Prefix string

	// Middleware wraps the op's handler. nil means none — the common case, and
	// the one that costs nothing.
	Middleware Middleware
}

// OpTarget is a place a typed op can be declared: the [App], or any [Router] of
// it — a Group, or the result of With. [Get] and friends take one of these, so
// `zip.Get(app, …)` and `zip.Get(v1, …)` are the same declaration with the same
// meaning, and a group-structured app does not spell its prefix out per route to
// have typed ops.
//
// Every Router is one, so a Router that DECORATES another must implement it,
// and must implement it faithfully. A decorator that gates the routes it
// registers has to return that gate in Middleware, or a typed op declared
// through it is registered ungated — the decorator's whole purpose, silently
// skipped. Embedding the wrapped Router and overriding this method is the
// shape: the embedded one answers for everything else.
type OpTarget interface {
	OpScope() OpScope
}

// Router is the path-mounting surface shared by *App and Group.
// All concrete routes flow through toFiberHandler — fiber.Ctx is
// the underlying type the framework's users never see directly.
//
// That last sentence is why nothing here names fiber. This interface used to
// carry `Fiber() *fiber.App` "for one-off escape", and it cost far more than it
// bought: a Router is the type a DECORATOR implements — the whole point of
// [OpTarget]'s note above — and a decorator wraps something, so it has no
// *fiber.App of its own to return. Requiring one made the interface
// unimplementable outside this package. Every real decorator either delegated
// the method blindly (zip's own wrapRouter did exactly that) or could not
// satisfy it at all, which is what stalled the v1.19 adoption in hanzoai/cloud
// and hanzoai/commerce.
//
// The escape hatch itself was never the problem; putting it on the ABSTRACTION
// was. It lives on the concrete [App] — see [App.Fiber] — where a caller that
// genuinely needs the underlying router still reaches it, and where wanting it
// forces you to hold an *App rather than quietly widening every Router in the
// estate. Prefer [App.Routes] and [App.Test], which serve the two things
// callers actually reached through Fiber() for.
type Router interface {
	// Every Router is somewhere a typed op can be declared, so `zip.Get(v1, …)`
	// takes a Group as readily as it takes the App.
	OpTarget

	// Use is the ONE composition verb, so it takes a [Component] — middleware,
	// or another App included by reference. It is the only signature that
	// widened; every route method below still takes ...Handler.
	Use(cs ...Component) Router

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

	// Group returns the *App it creates, because a group IS an app with a
	// prefix — the same definition kind, included by reference like any other.
	// One mechanism covers "a scope" and "a sub-application"; frameworks that
	// grew them separately have two.
	Group(prefix string, handlers ...Handler) *App
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
