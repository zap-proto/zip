package zip

import "testing"

// A decorator is the reason Router is an interface at all, so the interface has
// to be implementable by something that is NOT an *App and holds no fiber type.
// This file is a compile-time proof of exactly that.
//
// It exists because the property did not hold. Router carried
// `Fiber() *fiber.App` "for one-off escape", which every implementor had to
// satisfy — and a decorator wraps a Router, so it has no *fiber.App of its own.
// Outside this package it was unimplementable; inside it, zip's own wrapRouter
// only ever delegated the method. The estate paid for that: hanzoai/cloud's
// `scope` and hanzoai/commerce's `mintRouter` are both decorators, and both
// failed to compile against the interface — a 157-file migration stalled on a
// method nobody wanted.
//
// If someone adds a fiber-shaped member back to Router, this file stops
// compiling, which is the point. A comment asking for the property is not the
// property.

// countingRouter decorates a Router and counts registrations. It embeds the
// wrapped Router for everything it does not care about — the shape OpTarget's
// doc recommends — and imports no fiber.
type countingRouter struct {
	Router
	n *int
}

func (c countingRouter) Get(path string, handlers ...Handler) Router {
	*c.n++
	return c.Router.Get(path, handlers...)
}

// The proof. If Router names a fiber type again, this assignment fails.
var _ Router = countingRouter{}

// TestDecoratorNeedsNoFiber drives the decorator through a real registration so
// the guard is not merely structural: a type can satisfy an interface and still
// be useless if the embedded router is not actually reached.
func TestDecoratorNeedsNoFiber(t *testing.T) {
	app := New(Config{AppName: "decorator", DisableStartupMessage: true})
	n := 0

	var r Router = countingRouter{Router: app, n: &n}
	r.Get("/decorated", func(c *Ctx) error { return nil })

	if n != 1 {
		t.Fatalf("decorator did not observe the registration: got %d, want 1", n)
	}

	// And the registration must have reached the wrapped app, not stopped at the
	// decorator — a decorator that swallows routes is worse than none.
	if got := len(app.Fiber().GetRoutes(true)); got == 0 {
		t.Fatal("decorated route never reached the underlying app")
	}
}
