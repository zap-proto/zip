package zip

import "github.com/zap-proto/fiber/v3"

// Middleware is a composable request transformer in the classic wrapping form:
// given the next Handler it returns a Handler that runs around it. This is a
// DIFFERENT tool from Use — they do different jobs and compose freely:
//
//   - Use(Handler...) registers GLOBAL / prefix middleware. It runs for every
//     matched route (or every route under a Group) in DECLARATION order and
//     chains via c.Next(). Reach for it for ambient cross-cutting concerns
//     that apply broadly: logging, recovery, request-id.
//
//   - Middleware + With + Chain wrap ONE leaf handler explicitly, at
//     registration time, with no c.Next() indirection. Reach for it when a
//     specific endpoint needs a specific pipeline:
//
//     app.With(RateLimit, CSRF).Post("/v1/keys", mintKey)
//
//     wraps mintKey as RateLimit(CSRF(mintKey)): RateLimit is outermost and
//     runs first, CSRF next, the handler last; any layer short-circuits by
//     returning without calling next.
//
// A Middleware body is written by hand, no framework glue:
//
//	func RequireCSRF(next zip.Handler) zip.Handler {
//	    return func(c *zip.Ctx) error {
//	        if !validCSRF(c) {
//	            return c.String(403, "bad csrf") // short-circuit
//	        }
//	        return next(c) // continue
//	    }
//	}
type Middleware = func(next Handler) Handler

// Chain composes middleware left-to-right into one Middleware. Chain(a, b, c)
// nests as a(b(c(handler))): a is outermost (runs first inbound, last
// outbound), c innermost, wrapping the handler directly. Chain() with no
// arguments is the identity middleware.
func Chain(mw ...Middleware) Middleware {
	return func(next Handler) Handler {
		for i := len(mw) - 1; i >= 0; i-- {
			next = mw[i](next)
		}
		return next
	}
}

// wrapRouter decorates an inner Router so every leaf route it registers has its
// Handler wrapped by a Middleware chain first (chi's With idiom). Non-leaf
// operations delegate to the inner Router; Group and Route propagate the chain
// so leaves registered beneath them stay wrapped. Registration still flows
// through the same fiber path as any other route, so specificity precedence is
// unchanged — only the leaf handler is pre-wrapped.
type wrapRouter struct {
	inner Router
	wrap  Middleware
}

func (w *wrapRouter) Use(handlers ...Handler) Router { w.inner.Use(handlers...); return w }

func (w *wrapRouter) Get(p string, h Handler) Router     { w.inner.Get(p, w.wrap(h)); return w }
func (w *wrapRouter) Post(p string, h Handler) Router    { w.inner.Post(p, w.wrap(h)); return w }
func (w *wrapRouter) Put(p string, h Handler) Router     { w.inner.Put(p, w.wrap(h)); return w }
func (w *wrapRouter) Patch(p string, h Handler) Router   { w.inner.Patch(p, w.wrap(h)); return w }
func (w *wrapRouter) Delete(p string, h Handler) Router  { w.inner.Delete(p, w.wrap(h)); return w }
func (w *wrapRouter) Head(p string, h Handler) Router    { w.inner.Head(p, w.wrap(h)); return w }
func (w *wrapRouter) Options(p string, h Handler) Router { w.inner.Options(p, w.wrap(h)); return w }
func (w *wrapRouter) All(p string, h Handler) Router     { w.inner.All(p, w.wrap(h)); return w }

func (w *wrapRouter) Group(prefix string, handlers ...Handler) Router {
	return &wrapRouter{inner: w.inner.Group(prefix, handlers...), wrap: w.wrap}
}

func (w *wrapRouter) Route(prefix string, fn func(r Router)) Router {
	return &wrapRouter{
		inner: w.inner.Route(prefix, func(r Router) { fn(&wrapRouter{inner: r, wrap: w.wrap}) }),
		wrap:  w.wrap,
	}
}

func (w *wrapRouter) Fiber() fiber.Router { return w.inner.Fiber() }
