package zip

import (
	"fmt"

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
	// occ is the walk this generation was built from, RETAINED so the reducers
	// serving it — the OpenAPI endpoint, the MCP list, llms.txt — reuse it
	// rather than rewalking per request.
	occ []occurrence
	// routes is how many routes this generation's router held the moment it was
	// built. A later rebuild compares it against the router's CURRENT count to
	// catch a caller that registered on the value [App.Fiber] handed out — see
	// checkForeignRoutes.
	routes int
	// The projections, computed once, read without a lock forever after.
	router *fiber.App
	serve  fasthttp.RequestHandler
	ops    []*registeredOp
	hosts  []*App
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
	// Pipeline, in order, or nothing publishes: walk once, then BOTH validation
	// stages complete with trails, then the reducers.
	occ, err := walk(a)
	if err != nil {
		return nil, err
	}
	if err := structural(occ); err != nil {
		return nil, err
	}
	if err := derived(occ); err != nil {
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
	g.hosts = computeHosts(a, occ)
	// Adopt: every plugin in this composition learns which app is the ROOT of it.
	// This is the only place that knows, and a plugin needs it to ask the two
	// questions its own definition cannot answer — how many processes are running
	// across the whole host, and what the host's ceiling is.
	for _, h := range g.hosts {
		h.plugMu.Lock()
		for _, pl := range h.plugins {
			pl.owner.Store(a)
		}
		h.plugMu.Unlock()
	}
	g.routes = len(g.router.GetRoutes(false))
	return g, nil
}

// install makes g live and freezes every definition it reaches. Freezing is
// what earns the lock-free read: a definition that cannot change cannot
// invalidate a projection taken from it.
// checkForeignRoutes refuses to discard work a caller did on the router.
//
// [App.Fiber] hands out the CURRENT generation's router, and a generation is a
// projection: the next build materialises a fresh one and the old is dropped. So
//
//	app.Fiber().Use(cors.New(...))
//
// registered CORS on a value that the next registration silently threw away — no
// error, no panic, no CORS headers, on a policy whose entire job is to be there.
// A middleware seam lost silently is the exact failure this design exists to
// prevent, and it was arriving through the one door left open.
//
// A doc line is weak medicine for that, so this is a mechanism: the generation
// records how many routes its router had when it was built, and a rebuild that
// finds MORE refuses rather than discarding them. It cannot see a Use that
// matched nothing, but it catches every registration, which is the shape the
// footgun actually takes.
func (a *App) checkForeignRoutes(site callsite) {
	prev := a.live.Load()
	if prev == nil || prev.router == nil {
		return
	}
	if now := len(prev.router.GetRoutes(false)); now != prev.routes {
		panic(fmt.Sprintf("zip: %s: %d route(s) were registered directly on the value App.Fiber() "+
			"returned, and rebuilding the program would discard them.\n\tFiber() hands out the "+
			"CURRENT generation's router; a generation is a PROJECTION, so the next build "+
			"materialises a fresh one.\n\tRegister on the App instead — app.Use(mw) for "+
			"middleware, app.Get(path, h) for a route — so it survives every generation.",
			site, now-prev.routes))
	}
}

func (a *App) install(g *generation) {
	site := here(1)
	for _, child := range g.hosts {
		child.freeze(site)
	}
	a.freeze(site)
	a.live.Store(g)
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

	a.checkForeignRoutes(site)
	g, err := a.build()
	if err != nil {
		return fmt.Errorf("zip: %s: composition refused, generation %d still serving: %w",
			site, a.liveN(), err)
	}
	a.install(g)
	ok = true
	return nil
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
