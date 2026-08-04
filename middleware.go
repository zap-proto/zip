package zip

import (
	"fmt"
)

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
// Handler wrapped by a Middleware chain first (see With). Non-leaf
// operations delegate to the inner Router; Group propagates the chain so
// leaves registered beneath it stay wrapped. Registration still flows
// through the same fiber path as any other route, so specificity precedence is
// unchanged — only the leaf handler is pre-wrapped.
type wrapRouter struct {
	inner *App
	wrap  Middleware
	// shadow is [App.Shadow]'s scope: every route registered through this
	// decorator YIELDS its address. Carried here rather than on inner for the
	// same reason wrap is — the decorator is the scope, and inner is an App that
	// other scopes and the bare receiver also register on.
	shadow bool
}

// Use accepts middleware, which it wraps like any other leaf. It REFUSES a
// definition: With composes a chain around leaves registered THROUGH it, and a
// definition's leaves belong to the definition — wrapping them would mean
// editing an App that other hosts may also compose. Silently composing it
// ungated is the one thing that must not happen, since the chain people reach
// for With to install is usually the gate.
func (w *wrapRouter) Use(cs ...Component) Router {
	for _, c := range cs {
		if def, isApp := c.(*App); isApp {
			panic(fmt.Sprintf("zip: With(...).Use(%s): a scoped chain cannot wrap a definition's "+
				"own leaves — put the definition in a Group and scope the chain there: "+
				"app.With(mw).Group(prefix).Use(def)", def.who()))
		}
	}
	w.inner.Use(cs...)
	return w
}

func (w *wrapRouter) Get(p string, hs ...Handler) Router {
	w.inner.method("GET", p, w.wrapChain(hs), w.yields())
	return w
}
func (w *wrapRouter) Post(p string, hs ...Handler) Router {
	w.inner.method("POST", p, w.wrapChain(hs), w.yields())
	return w
}
func (w *wrapRouter) Put(p string, hs ...Handler) Router {
	w.inner.method("PUT", p, w.wrapChain(hs), w.yields())
	return w
}
func (w *wrapRouter) Patch(p string, hs ...Handler) Router {
	w.inner.method("PATCH", p, w.wrapChain(hs), w.yields())
	return w
}
func (w *wrapRouter) Delete(p string, hs ...Handler) Router {
	w.inner.method("DELETE", p, w.wrapChain(hs), w.yields())
	return w
}
func (w *wrapRouter) Head(p string, hs ...Handler) Router {
	w.inner.method("HEAD", p, w.wrapChain(hs), w.yields())
	return w
}
func (w *wrapRouter) Options(p string, hs ...Handler) Router {
	w.inner.method("OPTIONS", p, w.wrapChain(hs), w.yields())
	return w
}
func (w *wrapRouter) All(p string, hs ...Handler) Router {
	w.inner.method(methodAll, p, w.wrapChain(hs), w.yields())
	return w
}

// yields is the decorator's scope OR the App's own, because a decorator of a
// shadowed App is still shadowed — scopes compose, they do not replace.
func (w *wrapRouter) yields() bool { return w.shadow || w.inner.shadow }

// wrapChain wraps the FINAL handler (the terminal) with w.wrap and passes any
// preceding middleware through untouched — With() composes around the leaf,
// never around the chain's middleware.
func (w *wrapRouter) wrapChain(hs []Handler) []Handler {
	if len(hs) == 0 || w.wrap == nil {
		// nil wrap is a Shadow-only scope: it yields addresses and composes
		// nothing around them.
		return hs
	}
	out := append([]Handler(nil), hs...)
	out[len(out)-1] = w.wrap(out[len(out)-1])
	return out
}

// OpScope carries the wrap down to the typed op, so `zip.Post(app.With(CSRF), …)`
// gates the op the same way `app.With(CSRF).Post(…)` gates an untyped route.
// Delegating without it would drop the middleware silently, which for the gates
// people reach for With to install is a hole, not an inconvenience — and it is
// why OpTarget is implementable from outside this package at all.
func (w *wrapRouter) OpScope() OpScope {
	s := w.inner.OpScope()
	s.Shadow = w.yields()
	if w.wrap == nil {
		return s
	}
	if s.Middleware == nil {
		s.Middleware = w.wrap
		return s
	}
	s.Middleware = Chain(s.Middleware, w.wrap)
	return s
}

// Group carries the wrap ONTO the group rather than around a decorator of it,
// because a group is an App and an App can hold its own wrap. Every leaf in the
// scope is wrapped, including ones registered on the returned App later — which
// is what a scoped With has to mean, and is why this cannot just delegate and
// drop the chain the way a decorator that returned a bare group would.
func (w *wrapRouter) Group(prefix string, handlers ...Handler) Router {
	g := w.inner.group(here(1), prefix, handlers...)
	// A group of a shadowed scope is a shadowed group, and every leaf beneath it
	// inherits that — including leaves registered on the returned App later,
	// which is what a SCOPE has to mean and is why this cannot be a decorator
	// that only sees the calls made through it.
	g.shadow = g.shadow || w.shadow
	if w.wrap == nil {
		return g
	}
	if g.wrap == nil {
		g.wrap = w.wrap
	} else {
		g.wrap = Chain(g.wrap, w.wrap)
	}
	return g
}
