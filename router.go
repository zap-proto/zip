package zip

import (
	"github.com/zap-proto/fiber/v3"
)

// Router is the path-mounting surface shared by *App and Group.
// All concrete routes flow through toFiberHandler — fiber.Ctx is
// the underlying type the framework's users never see directly.
type Router interface {
	Use(handlers ...Handler) Router

	// Route registration takes ONE chain in gin/express order: zero or more
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

// splitChain adapts one registration chain — middleware first, the final
// handler LAST (gin/express order) — to fiber's variadic signature. fiber
// executes route handlers in ARGUMENT order (the first argument enters first
// and Next() descends), so the chain passes through verbatim: first element,
// then the rest. Registering a route with no handler is a programmer error
// and panics at boot, never at request time.
// normPath maps the empty leaf to the group root: Get("") on a Group("/x")
// means "/x" (the gin/express idiom). fiber never matches an empty path, so
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
		c := &Ctx{fc: fc, app: app, log: app.logger}
		return h(c)
	}
}
