package zip

import (
	"cmp"
	"fmt"
	"strings"

	fiber "github.com/zap-proto/fiber/v3"
)

// Graft composes a child App into this one, IN PROCESS: the parent's router
// learns the child's SHAPE, the child's router keeps the child's BEHAVIOUR.
//
//	app.Graft(iamserver.NewApp(db))
//
// It is the fifth verb in one set, and the one that was missing. Listen serves
// here; Mount delegates there; Add composes a registrar; Load composes a
// binary; Graft composes an APP. Until it existed the only way to put a live
// *App behind a host's router was [AdaptNetHTTP], which takes an http.Handler —
// so the App went in and a bare closure came out, and with it went the op
// registry. Five projections die at that closure: the OpenAPI document, the MCP
// tool list, the CLI commands, the by-name call plane and the [Declaration].
// A host could then publish only the wildcard it hung the closure on.
//
// # No prefix argument
//
// The child's paths are its OWN — a real identity provider owns three disjoint
// prefixes (/v1/iam, /login/oauth, /.well-known) and one prefix string cannot
// say that. A host that wants a child somewhere else builds the child on a
// [App.Group] of that prefix, which is the prefixing mechanism that already
// exists; a second one would be two ways to do one thing.
//
// # Delegation, not route-copying
//
// For every pattern the child declares, the parent registers that exact pattern
// pointing at ONE handler that runs the child's own router on the same
// fasthttp request. The child re-matches internally, with its own Use chain,
// its own error handler and its own config.
//
// Copying the child's route handlers instead would drop its middleware —
// [Declaration] projects GetRoutes(true), which filters Use entries out — and
// a service whose entire authentication seam is app.Use(guard) would arrive
// with the guard silently gone. Copying the Use entries too is worse: a Use
// registered with no path, grafted onto the parent, would gate every route in
// the host binary. Delegation has neither failure, because middleware stays
// inside the app that declared it.
//
// It is also strictly CHEAPER than the adapter it replaces: no net/http round
// trip, so the adapter's ~5% goes, and the fasthttp user-context stops being
// dropped. The cost is one extra route lookup per request — no allocation, no
// protocol conversion.
//
// And it NARROWS the surface. A wildcard swallows every unknown path under its
// prefix; Graft registers only the patterns the child declares, so an
// undeclared path falls through to the parent, which is what a host wants.
//
// # What the parent learns
//
// Each child op is appended to the parent's registry as a COPY carrying
// Origin = the child's AppName. The copy is what keeps the child's own
// document unchanged — a child served standalone still describes its types
// under their plain names. op.invoke closes over the child's handler AND the
// child's Authorizer, so the child's authorization keeps running on grafted
// ops over REST and MCP alike; the parent never silently re-authorizes a
// child's op under its own rules. Paths are untouched: an op's Path is already
// the whole absolute path, and rewriting it would name a route that does not
// exist. operationIds are untouched: an id is a published SDK method name, and
// rewriting it at compose time would make an SDK method name a function of
// where the app is deployed — uniqueness comes from the address refusal below,
// which subsumes it.
//
// Named types ARE qualified — "<origin>.<Type>", always, not on collision.
// Qualifying only on collision would make a published type name a function of
// who else is in the room: add a schema to one app and another app's type
// silently renames. See schemaRegistry.origin.
//
// # All-or-nothing
//
// Before registering anything, Graft checks every METHOD+pattern each child
// declares against what the parent already holds and against the other
// children in the same call. Any overlap returns one error naming both
// claimants and registers NOTHING — a half-grafted app is worse than a refused
// one. It reads the ROUTER, not the document: the addresses that collide in
// practice are liveness and health paths that no document publishes.
//
// # Lifecycle
//
// Graft must run before [App.Listen] — the document, the tool list and the call
// plane are rendered once, from the registry, at prepare time, so an op grafted
// after that would serve but never describe. It prepares the child (idempotent),
// does not register the child's control plane ([Declaration] excludes it, so the
// parent keeps its own /docs, MCP door and op plane), and adopts the child's
// teardown so a graft cannot leak the child's store.
func (a *App) Graft(children ...*App) error {
	if len(children) == 0 {
		return nil
	}
	if a.prepared.Load() {
		return fmt.Errorf("zip: Graft must run before Listen — the document, the tool list " +
			"and the call plane are rendered once from the registry, so an op grafted after " +
			"that would serve and never describe")
	}

	// PASS 1 — no mutation. Every address every child wants, checked against the
	// parent's router and against its siblings, so a refusal costs nothing.
	held := map[string]string{}
	host := cmp.Or(a.cfg.AppName, "host")
	for _, r := range a.fiber.GetRoutes(true) {
		if r.Method == "" || r.Path == "" {
			continue
		}
		held[addrKey(r.Method, r.Path)] = host
	}

	type graftee struct {
		name  string
		decl  Declaration
		child *App
	}
	grafts := make([]graftee, 0, len(children))
	var refused []string
	for _, child := range children {
		if child == nil {
			return fmt.Errorf("zip: Graft: nil child")
		}
		name := child.cfg.AppName
		if name == "" {
			return fmt.Errorf("zip: Graft: a child with no AppName cannot be grafted — " +
				"its name is what qualifies its types in the composed document")
		}
		decl := child.Declaration() // prepares the child; excludes zip's control plane
		for _, r := range decl.Routes {
			k := addrKey(r.Method, r.Pattern)
			if owner, dup := held[k]; dup {
				refused = append(refused, fmt.Sprintf(
					"%q is already claimed by %q — %q cannot have it too",
					r.Method+" "+r.Pattern, owner, name))
				continue
			}
			held[k] = name
		}
		grafts = append(grafts, graftee{name: name, decl: decl, child: child})
	}
	if len(refused) > 0 {
		return fmt.Errorf("zip: Graft refused, nothing registered:\n\t%s", strings.Join(refused, "\n\t"))
	}

	// PASS 2 — register. Every check has passed, so nothing below can fail.
	for _, g := range grafts {
		// One handler value for every one of the child's patterns: the child's
		// own router, run on the request the parent already has.
		serve := g.child.fiber.Handler()
		delegate := func(c fiber.Ctx) error {
			serve(c.RequestCtx())
			return nil
		}
		for _, r := range g.decl.Routes {
			a.fiber.Add([]string{r.Method}, r.Pattern, delegate)
		}
		for _, op := range g.child.registry {
			// A COPY: the parent's registry is its own value, so composing a
			// child never edits what the child says about itself. An op that
			// already names its declarer keeps that name — a graft two levels
			// deep still credits the app that wrote the type.
			c := *op
			if c.Origin == "" {
				c.Origin = g.name
			}
			if len(c.Tags) == 0 {
				// Derived, never invented: a grafted product is a named group
				// in the parent's document instead of an untagged blob.
				c.Tags = []string{c.Origin}
			}
			a.registry = append(a.registry, &c)
		}
		a.OnShutdown(g.child.ShutdownWithContext)
		a.logger.Info("zip grafting", "child", g.name,
			"routes", len(g.decl.Routes), "ops", len(g.child.registry))
	}
	return nil
}

// addrKey is one address as the collision check sees it: METHOD + the router's
// own spelling of the pattern, with "/x/" and "/x" one address and not two —
// the same normalisation [App.claim] applies to a mounted prefix.
func addrKey(method, pattern string) string {
	p := normPath(pattern)
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return method + " " + p
}
