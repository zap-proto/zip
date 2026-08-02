package zip

import (
	"fmt"
	"sync"

	"github.com/valyala/fasthttp"
	fiber "github.com/zap-proto/fiber/v3"
)

// A program is not mutable. It is VERSIONED.
//
// The runtime requirement is real: plugins load lazily, routing updates on
// demand, subsystems get dropped and reloaded. The wrong conclusion is that the
// program must therefore be editable while it serves. A generation is a sealed,
// immutable program together with everything projected from it, and the live
// system is one atomic pointer to the current one.
//
// # Build then swap, never mutate
//
// A composition change constructs generation N+1 from the current entry list
// plus or minus the changed references, runs the whole walk and every
// validation over it, and swaps ONLY on success. A plugin whose patterns
// collide with the live set fails the build, with breadcrumbs, and the old
// generation keeps serving. Load and reload are transactional: a bad plugin
// cannot take down routing. That is the property that makes dynamic composition
// safe, and it is unreachable if composition edits a live structure in place.
//
// # Requests are generation-pinned
//
// A request loads the pointer once, on arrival, and completes on that
// generation whatever happens afterwards. In-flight requests never observe a
// half-applied change, because there is no such state to observe: the new
// generation is complete before the pointer moves. Reads are lock-free and
// there is no lock on the served path in any phase.
//
// # What freezes
//
// A definition freezes when it first appears in a BUILT generation. Mutating a
// frozen App panics, and the panic means exactly one thing: go through a
// generation. This aligns with Go's plugin mechanics rather than fighting them
// — a reloaded .so yields a NEW *App from a new plugin.Open, so versions are
// distinct definitions with distinct pointers, which is precisely what the
// walk's pointer-identity rule wants.
type generation struct {
	// n counts generations from 0, so a diagnostic can say which one answered.
	n uint64
	// occ is the walk this generation was built from — the canonical form every
	// projection below was reduced out of.
	occ []occurrence
	// The projections, computed once, read without a lock forever after.
	router *fiber.App
	serve  fasthttp.RequestHandler
	ops    []*registeredOp
	ctl    []route
}

// Generation reports which generation is live, and whether one is.
func (a *App) Generation() (uint64, bool) {
	g := a.live.Load()
	if g == nil {
		return 0, false
	}
	return g.n, true
}

// build computes the next generation from the current program WITHOUT
// installing it. Nothing it does is observable, so a failed build costs the
// live system nothing.
func (a *App) build() (*generation, error) {
	occ, err := walk(a)
	if err != nil {
		return nil, err
	}
	var n uint64
	if prev := a.live.Load(); prev != nil {
		n = prev.n + 1
	}
	g := &generation{n: n, occ: occ, ctl: a.ctl}
	g.router = a.materialise(occ, g.ctl)
	g.serve = g.router.Handler()
	g.ops = composeOps(occ)
	return g, nil
}

// install makes g live and freezes every definition it reaches. Freezing is
// what earns the lock-free read: a definition that cannot change cannot
// invalidate a projection taken from it.
// serving is every App with a live generation. A definition does not know where
// it is included — that is the point of inclusion by reference — so when a
// composition change lands on a CHILD, the only way to find the servers affected
// is to ask the servers. There are a handful of them per process, and this runs
// at composition time, never per request.
var serving sync.Map // *App -> struct{}

func (a *App) install(g *generation) {
	site := here(1)
	for _, o := range g.occ {
		if child, ok := o.app(); ok {
			child.freeze(site)
		}
	}
	a.freeze(site)
	a.live.Store(g)
	serving.Store(a, struct{}{})
	// The MCP door serves pre-rendered bytes — that is what makes tools/list free
	// — so a new generation has to re-render them or an agent keeps reading the
	// previous composition's tool set.
	if a.mcpList.Load() != nil {
		a.renderTools()
	}
}

func (a *App) freeze(site callsite) {
	if a.frozen.CompareAndSwap(false, true) {
		a.freezeSite = site
	}
}

// Frozen reports whether this definition has appeared in a built generation and
// can therefore no longer be edited in place.
func (a *App) Frozen() bool { return a.frozen.Load() }

// Include builds and installs the next generation with cs composed in.
//
// It is [App.Use] against a RUNNING system, and it is a different verb for one
// reason that is not cosmetic: it can fail. Use is a declaration written while
// nothing is serving, so it cannot conflict with anything live and returns the
// receiver for chaining. Include is a transaction against a live route set, so
// it returns the error and — on error — changes nothing at all.
//
//	if err := app.Include(billingPlugin); err != nil {
//	    log.Error("plugin refused, previous generation still serving", "err", err)
//	}
func (a *App) Include(cs ...Component) error {
	return a.transact(here(1), func() { a.compose(here(2), cs) })
}

// Drop builds and installs the next generation with every entry that references
// defs removed. Identity is the POINTER, which is what makes this expressible at
// all: an entry is a reference, and the definition is the thing being named.
//
// It drops every reference THE RECEIVER HOLDS. That answers the shared-definition
// question the obvious way once you see whose program is being edited: an entry
// belongs to the app that wrote it, so a host can only drop what the host
// included. A definition reached through a group is referenced by the GROUP, so
// dropping it means dropping the group — and a definition that some other host
// also includes keeps serving there, which is required, not a limitation:
//
//	billing := billingApp()
//	v1 := app.Group("/v1"); v1.Use(billing)
//	app.Drop(billing)  // no-op: app never referenced billing, v1 did
//	app.Drop(v1)       // stops serving /v1/...
//
// Within the receiver it drops ALL references: a definition the receiver
// included at two prefixes is one thing serving in two places, and "stop
// serving billing" is not a question about one of them.
//
// Routing-level only. Go's plugin package has no Close and never unloads a
// .so — code and package state live for the process. To reclaim memory, run the
// subsystem out of process behind [App.Mount] instead; that is what the one
// remaining deployment verb is for.
func (a *App) Drop(defs ...*App) error {
	site := here(1)
	return a.transact(site, func() {
		drop := make(map[*App]bool, len(defs))
		for _, d := range defs {
			drop[d] = true
		}
		kept := a.entries[:0:0]
		for _, e := range a.entries {
			if child, ok := e.n.(*App); ok && drop[child] {
				continue
			}
			kept = append(kept, e)
		}
		a.entries = kept
		version.Add(1)
	})
}

// transact is the ONE path a composition change takes once something is live:
// edit, build, and install — or put the entry list back exactly as it was and
// return why. There is no partial outcome, because the edit is only ever
// observed through a generation and a generation is only ever installed whole.
func (a *App) transact(site callsite, edit func()) error {
	a.buildMu.Lock()
	defer a.buildMu.Unlock()

	before := a.entries
	adopted := len(a.hooks)
	// DEFERRED, because a build can PANIC as well as fail: fiber refuses an
	// unknown HTTP method by panicking, and a Declaration is a build input from
	// another team. A straight-line rollback unwinds past itself and leaves the
	// edit in a.entries, so the next composition — a valid one — panics on the
	// poison. "The old generation keeps serving" has to hold for a panic too.
	ok := false
	defer func() {
		if ok {
			return
		}
		a.entries = before
		// compose() adopts a child's teardown BEFORE the build can refuse it; a
		// host that refused a plugin must not own that plugin's shutdown.
		a.hookMu.Lock()
		if len(a.hooks) > adopted {
			a.hooks = a.hooks[:adopted]
		}
		a.hookMu.Unlock()
		version.Add(1)
	}()

	a.entries = append(a.entries[:0:0], a.entries...) // copy: rollback must be total
	edit()

	// Every server this edit can reach must be rebuilt, not just the receiver.
	// Include on a group used to return nil and change nothing a host served —
	// while the freeze panic RECOMMENDED Include — because transact installed
	// onto the receiver and a group has no listener.
	targets := a.affected()
	built := make([]*generation, len(targets))
	for i, t := range targets {
		g, err := t.build()
		if err != nil {
			return fmt.Errorf("zip: %s: composition refused, generation %d still serving: %w",
				site, t.liveN(), err)
		}
		built[i] = g
	}
	// Every build succeeded, so nothing below can fail: the swap is the only
	// observable moment, and it is one atomic store per server.
	for i, t := range targets {
		t.install(built[i])
	}
	ok = true
	return nil
}

// affected is the receiver if it is itself a server, plus every live server that
// reaches it. Deduplicated, receiver first.
func (a *App) affected() []*App {
	out := []*App{}
	seen := map[*App]bool{}
	if a.live.Load() != nil {
		out, seen[a] = append(out, a), true
	}
	serving.Range(func(k, _ any) bool {
		root := k.(*App)
		if seen[root] || root.live.Load() == nil {
			return true
		}
		for _, o := range root.plan() {
			if def, ok := o.app(); ok && def == a {
				out, seen[root] = append(out, root), true
				return true
			}
		}
		return true
	})
	if len(out) == 0 {
		out = append(out, a) // nothing live yet: build the receiver, as before
	}
	return out
}

func (a *App) liveN() uint64 {
	n, _ := a.Generation()
	return n
}

// compose appends components without the seal check, for use inside a
// transaction where the entry list is a private copy.
func (a *App) compose(site callsite, cs []Component) {
	for _, c := range cs {
		switch v := c.(type) {
		case nil:
		case Handler:
			if v == nil {
				continue
			}
			a.entries = append(a.entries, entry{n: v, site: site})
		case *App:
			if v == nil {
				panic(fmt.Sprintf("zip: %s: Include(nil *App)", site))
			}
			a.entries = append(a.entries, entry{n: v, site: site})
			a.OnShutdown(v.ShutdownWithContext)
		default:
			panic(fmt.Sprintf("zip: %s: %T is not a Component", site, c))
		}
	}
	version.Add(1)
}

// includeRoutes is Include for routes rather than Components — the door a
// plugin mounted at run time comes through ([App.load]). Same transaction, same
// all-or-nothing.
func (a *App) includeRoutes(site callsite, rs ...route) error {
	if a.live.Load() == nil {
		// Nothing is serving yet, so this is ordinary composition.
		for _, r := range rs {
			a.addRoute(site, r)
		}
		return nil
	}
	return a.transact(site, func() {
		for _, r := range rs {
			a.entries = append(a.entries, entry{n: r, site: site})
		}
		version.Add(1)
	})
}

// pinned is the handler Listen serves: ONE pointer load per request, then that
// request belongs to that generation until it finishes.
func (a *App) pinned() fasthttp.RequestHandler {
	return func(rc *fasthttp.RequestCtx) {
		a.live.Load().serve(rc)
	}
}

// liveOrBuild returns the live generation, building an uninstalled one if
// nothing is serving yet.
//
// Pre-build inspection recomputes per call and deliberately does not install:
// a codegen step, a test or a doc generator that looks at a program must not
// freeze it and turn the next legitimate Use into a panic about a generation
// nobody asked for.
func (a *App) liveOrBuild() *generation {
	if g := a.live.Load(); g != nil {
		return g
	}
	a.buildMu.Lock()
	defer a.buildMu.Unlock()
	if g := a.live.Load(); g != nil {
		return g
	}
	if a.draft != nil && a.draftAt == version.Load() {
		return a.draft
	}
	at := version.Load() // BEFORE the build: an append during it must invalidate
	g, err := a.build()
	if g == nil {
		g = &generation{router: fiber.New(a.fiberConfig())}
	}
	// The error is REMEMBERED, not swallowed. An invalid program used to render
	// as an empty one — Registry() 0 ops, Declaration() 0 routes, and
	// `app declare` writing {"routes":[]} and exiting 0, which ships an empty
	// API document for a broken build. Accessors still answer (they cannot
	// return an error), but anything that PUBLISHES asks first: see project().
	a.draft, a.draftAt, a.draftErr = g, at, err
	return g
}
