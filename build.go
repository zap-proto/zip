package zip

import (
	"fmt"
	"sort"

	fiber "github.com/zap-proto/fiber/v3"
)

// Building is a projection, not a merge.
//
// Every artifact this package emits is a REDUCER over [walk]'s occurrence
// slice: the router, the op registry (and through it the OpenAPI document, the
// MCP tool list, the CLI commands and the by-name call plane), the conflict
// report and the staged-composition lint. Adding a projection means writing a
// reducer, never touching composition.

// methodAll is the sentinel [App.All] registers under: one entry answering
// every method, kept as one entry so the program says what was written. The
// router expands it; [App.Declaration] reads the expansion back off the router.
const methodAll = "ALL"

// plan is the occurrence slice for this app, memoised once the app is sealed.
//
// Sealed: computed exactly once under sync.Once, so [App.Registry] is race-free
// from serving goroutines — the OpenAPI endpoint and the MCP tool listing both
// call it per request — with no mutex on the served path.
//
// Unsealed: recomputed every call, and deliberately does NOT seal. Sealing on
// read would mean a codegen step, a test, or a `zipdoc` run turns the next
// perfectly legitimate Use into a panic about a seal nobody asked for.
func (a *App) plan() ([]occurrence, error) {
	if a.sealed.Load() {
		a.planOnce.Do(func() { a.planned, a.planErr = walk(a) })
		return a.planned, a.planErr
	}
	return walk(a)
}

// seal ends composition and is the ONE primitive that does so.
//
// Any entry point that begins RUNTIME EXECUTION calls this — [App.Listen] does
// today, and a future Serve, Run or Handler entry point calls this rather than
// a copy of it, which is the whole reason it is factored out of Listen. It is
// deliberately not exported and deliberately not called by any inspection path.
//
// Sealing PROPAGATES across the entire reachable graph, not just the app it was
// called on. Sealing only the root would leave
//
//	root.Use(users); go root.Listen(); users.Use(admin)
//
// racing exactly as before: the child is reachable, so the child is frozen.
// Sealed is MONOTONIC, so a definition already sealed under one parent and then
// included under another is fine — mount-after-seal is allowed, mutate-after-seal
// is not.
func (a *App) seal(site callsite) error {
	occ, err := walk(a)
	if err != nil {
		return err
	}
	for _, o := range occ {
		if child, ok := o.app(); ok {
			child.markSealed(site)
		}
	}
	a.markSealed(site)
	// The walk that validated IS the plan; sealing must not pay for a second one.
	a.planOnce.Do(func() { a.planned, a.planErr = occ, nil })
	return nil
}

func (a *App) markSealed(site callsite) {
	if a.sealed.CompareAndSwap(false, true) {
		a.sealSite = site
	}
}

// Sealed reports whether composition has ended for this app.
func (a *App) Sealed() bool { return a.sealed.Load() }

// Registry is the op registry as a PROJECTION: every typed op in the
// composition, at the path and under the id its occurrence gives it.
//
// This is the value that replaced five verbs with one. It used to be a field
// that [App.Graft] appended to at compose time, which is why composing an app
// needed a verb of its own and why type-erasing one into an http.Handler
// destroyed the OpenAPI document, the MCP tool list, the CLI commands, the
// by-name call plane and the [Declaration] all at once. A projection cannot be
// destroyed by composition, because composition no longer writes it.
//
// Keys on the OCCURRENCE for surface (path, operationId, tags) and on the
// DEFINITION for types: the *registeredOp's InType/OutType are the definition's
// own reflect.Types, so one Invoice struct included twice is one schema, not
// two identical copies under two names.
func (a *App) Registry() []*registeredOp {
	if a.sealed.Load() {
		a.regOnce.Do(func() { a.reg = a.composeOps() })
		return a.reg
	}
	return a.composeOps()
}

func (a *App) composeOps() []*registeredOp {
	occ, _ := a.plan() // a conflict does not stop enumeration; seal reports it
	out := make([]*registeredOp, 0, len(occ))
	for _, o := range occ {
		r, ok := o.route()
		if !ok || r.op == nil {
			continue
		}
		if o.ctx.depth == 0 && o.ctx.prefix == "" {
			// The served app's own op, at the root: untouched, so an app that
			// composes nothing publishes byte-for-byte what it declared.
			out = append(out, r.op)
			continue
		}
		c := *r.op // a copy: composing never edits what a definition says about itself
		c.Path = o.abs(r.path)
		c.OperationID = occurrenceID(o.ctx.prefix, opName(r.op))
		if c.Origin == "" {
			// Who DECLARED the type — a property of the code, never of where it
			// was deployed. An op that already names its declarer keeps that
			// name, so a definition two levels deep still credits its author.
			c.Origin = o.in.cfg.AppName
		}
		if len(c.Tags) == 0 && c.Origin != "" {
			c.Tags = []string{c.Origin}
		}
		out = append(out, &c)
	}
	return out
}

// router returns the materialised fiber router, building it if the program has
// moved since it was last built.
//
// Materialisation is a reducer like any other. It is rebuilt rather than
// patched, because a program is an ordered whole: an entry appended in the
// middle of a subtree has to land in the middle, and a router that could only
// be appended to would put it at the end.
//
// Two things this has to get right, both found by -race rather than by reading:
//
//   - Building on a READ is a write, and reads are concurrent. Before, the
//     router was constructed in New and never written again, so any number of
//     goroutines could call Fiber(); now a stale build repairs itself and that
//     repair needs the lock. It is not on the served path — Listen captures
//     Handler() once — so the lock costs nothing that matters.
//   - The version counter is PROCESS-wide, because an append to a child changes
//     a parent's meaning and a child does not know its parents. So a sealed
//     app must stop consulting it, or an unrelated App composing anything at
//     all would rebuild this one's router for no reason.
func (a *App) router() *fiber.App {
	a.buildMu.Lock()
	defer a.buildMu.Unlock()
	if a.fiber != nil && (a.sealed.Load() || a.builtAt == version.Load()) {
		return a.fiber
	}
	a.materialise(version.Load())
	return a.fiber
}

// materialise replays the occurrence slice onto a fresh router, in order.
//
// The replay IS the semantics, because this fiber fork resolves same-run routes
// by SPECIFICITY and treats every Use as a barrier between runs. Flattened
// preorder inlines an included definition's entries at its inclusion site, so
// replaying that order reproduces snapshot semantics exactly and for free:
// entries written after an inclusion site land after that subtree and cannot
// reach back into it.
//
// Middleware is placed by DEPTH, and this is the one place the two composition
// models actually differ on the wire:
//
//   - depth 0 — the app being served. Registered as router middleware, so it
//     runs for every request including ones that match no route (404 logging,
//     CORS preflight, recovery). Identical to what a single-app program has
//     always done.
//   - depth > 0 — declared inside an included definition. Composed into the
//     CHAIN of that subtree's own routes instead. Router middleware here would
//     escape the definition: a child's pathless app.Use(guard) registered on the
//     host's router is a barrier for the whole host binary, which is precisely
//     the failure [App.Graft]'s delegation existed to avoid. Composing it into
//     the subtree's routes keeps it inside the definition that declared it, and
//     costs the definition's middleware its coverage of unmatched paths — a
//     definition does not answer for addresses it does not declare.
func (a *App) materialise(v uint64) {
	occ, _ := a.plan()
	f := fiber.New(a.fiberConfig())
	for _, o := range occ {
		switch n := o.n.(type) {
		case Handler:
			if o.ctx.depth == 0 {
				f.Use(toFiberHandler(a, n))
			}
		case route:
			a.install(f, o.ctx.prefix, o.ctx.mw.included(), n)
		}
	}
	// zip's OWN routes go on last, and are not entries at all — see
	// [App.installControl].
	for _, c := range a.control_ {
		a.install(f, "", nil, c)
	}
	a.fiber = f
	a.builtAt = v
}

// installControl registers one of zip's own projection routes — the document,
// the docs page, the MCP door, the op plane, the declaration.
//
// These are NOT entries, and that is the whole point. A control route is a
// PROJECTION of the program (it serves what the program computes), so putting
// it in the program made two things wrong at once:
//
//   - it made rendering a projection a MUTATION, so sealing the program and
//     then asking it for its document panicked — [App.prepare] installs these,
//     and a projection must be readable after the seal;
//   - it made them inheritable. Inclusion reads a definition's whole program,
//     so a definition that had ever rendered its own document would hand the
//     host ITS document at the host's well-known path, and the composition
//     would publish the part as if it were the whole. That needed a special
//     case in the walk; belonging to the build instead, it needs none.
//
// They are replayed after the entries on every materialisation, which is the
// same position they have always occupied — prepare() has always run last.
func (a *App) installControl(r route) {
	a.buildMu.Lock()
	defer a.buildMu.Unlock()
	a.control_ = append(a.control_, r)
	if a.fiber != nil {
		a.install(a.fiber, "", nil, r)
	}
}

// install puts one route on one router: its absolute path, its subtree's
// middleware composed in front of it, then the handler the definition wrote.
//
// fiber runs a route's handlers in ARGUMENT order and advances through them on
// Next(), which is the same rule zip's own registration chain already uses — so
// composed middleware is passed as leading arguments and needs no wrapper type.
func (a *App) install(f *fiber.App, prefix string, mw []Handler, r route) {
	p := joinPath(prefix, r.path)
	args := make([]any, 0, len(mw)+len(r.chain)+1)
	for _, h := range mw {
		args = append(args, toFiberHandler(a, h))
	}
	// a, not the definition: every handler in one request must be handed the
	// same *Ctx, and requestCtx keys that on the app it was bound to.
	for _, h := range r.chain {
		args = append(args, toFiberHandler(a, h))
	}
	if r.serve != nil {
		args = append(args, r.serve)
	}
	if r.method == methodAll {
		f.All(p, args[0], args[1:]...)
		return
	}
	f.Add([]string{r.method}, p, args[0], args[1:]...)
}

// addRoute is the ONE place a route entry is appended, so every route method,
// every typed registration and zip's own control plane record the same thing in
// the same shape with a real call site.
func (a *App) addRoute(site callsite, r route) {
	a.appendEntry(entry{n: r, site: site})
}

// Lint reports staged composition: middleware appended to a receiver AFTER an
// App was included in it.
//
// It is not an error and not a warning tier. The pattern is INTENTIONAL when
// the two lines sit together —
//
//	app.Use(publicAPI)
//	app.Use(auth)
//	app.Use(privateAPI)
//
// — and a latent bug when they are written far apart, in different functions or
// different files, where the author of the later line cannot see which subtrees
// they just failed to cover. No walk can tell those two apart, because the
// difference is authorial intent and the only evidence of it is co-location.
// So this reports the fact and the two call sites and lets a human read them;
// promoting it to an error would break the legitimate case, and hiding it would
// leave the dangerous one silent.
//
// Reported per receiver, naming the included app, the middleware, and both
// sites.
func (a *App) Lint() []string {
	occ, _ := a.plan()
	seen := map[*App]bool{}
	var out []string
	for _, o := range occ {
		def := o.in
		if seen[def] {
			continue
		}
		seen[def] = true
		var included []entry
		for _, e := range def.entries {
			switch e.n.(type) {
			case *App:
				included = append(included, e)
			case Handler:
				for _, inc := range included {
					child, _ := inc.n.(*App)
					out = append(out, fmt.Sprintf(
						"zip: staged composition in %s: middleware at %s was appended after %s was included at %s — "+
							"it does not apply to that subtree; co-located this is intentional, far apart it is a missing seam",
						def.who(), e.site, child.who(), inc.site))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// fiberConfig is the App's Config as fiber wants it. One place, so New and
// every rematerialisation agree.
func (a *App) fiberConfig() fiber.Config {
	fcfg := fiber.Config{
		AppName:         a.cfg.AppName,
		BodyLimit:       a.cfg.BodyLimit,
		JSONEncoder:     jsonMarshal,
		JSONDecoder:     jsonUnmarshal,
		Concurrency:     a.cfg.Concurrency,
		ReadBufferSize:  a.cfg.ReadBufferSize,
		WriteBufferSize: a.cfg.WriteBufferSize,
	}
	if a.cfg.ServerHeader != "-" {
		fcfg.ServerHeader = a.cfg.ServerHeader
	}
	if a.cfg.ErrorHandler != nil {
		fcfg.ErrorHandler = a.cfg.ErrorHandler
	} else {
		fcfg.ErrorHandler = errorHandler
	}
	return fcfg
}
