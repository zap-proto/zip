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

// plan is the occurrence slice the live generation was built from, or a fresh
// walk when nothing is live yet.
func (a *App) plan() []occurrence { return a.liveOrBuild().occ }

// Registry is the op registry as a PROJECTION: every typed op in the
// composition, at the path and under the id its occurrence gives it.
//
// This is the value that replaced five verbs with one. It used to be a FIELD
// that [App.Graft] appended to at compose time, which is why composing an app
// needed a verb of its own and why type-erasing one into an http.Handler
// destroyed the OpenAPI document, the MCP tool list, the CLI commands, the
// by-name call plane and the [Declaration] all at once. A projection cannot be
// destroyed by composition, because composition no longer writes it.
//
// Lock-free once a generation is live — it is read from serving goroutines by
// the OpenAPI endpoint and the MCP tool listing, on every request.
//
// Keys on the OCCURRENCE for surface (path, operationId, tags) and on the
// DEFINITION for types: the *registeredOp's InType/OutType are the definition's
// own reflect.Types, so one Invoice struct included twice is one schema, not
// two identical copies under two names.
func (a *App) Registry() []*registeredOp { return a.liveOrBuild().ops }

// router returns the live generation's router, building a draft if nothing is
// live yet. It never installs — see [App.liveOrBuild].
func (a *App) router() *fiber.App { return a.liveOrBuild().router }

// composeOps reduces a walk to the op registry.
func composeOps(occ []occurrence) []*registeredOp {
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

// materialise replays an occurrence slice onto a fresh router, in order, and
// RETURNS it. It is pure with respect to the App — the caller decides whether
// the result becomes a generation — which is what lets a failed build be
// discarded without the live system ever having seen it.
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
func (a *App) materialise(occ []occurrence, ctl []route) *fiber.App {
	f := fiber.New(a.fiberConfig())
	for _, o := range occ {
		switch n := o.n.(type) {
		case Handler:
			if o.ctx.depth == 0 {
				f.Use(toFiberHandler(a, n))
			}
		case route:
			a.installRoute(f, o.ctx.prefix, o.ctx.mw.included(), n)
		}
	}
	// zip's OWN projection routes go on last, and are not entries at all: a
	// control route SERVES what the program computes, so putting it in the
	// program would make rendering a projection a mutation, and would make it
	// inheritable by any host that included this definition.
	for _, c := range ctl {
		a.installRoute(f, "", nil, c)
	}
	return f
}

// installRoute puts one route on one router: its absolute path, its subtree's
// middleware composed in front of it, then the handler the definition wrote.
//
// fiber runs a route's handlers in ARGUMENT order and advances through them on
// Next(), which is the same rule zip's own registration chain already uses — so
// composed middleware is passed as leading arguments and needs no wrapper type.
func (a *App) installRoute(f *fiber.App, prefix string, mw []Handler, r route) {
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
	occ := a.plan()
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

// hosts walks the composition and returns every App carrying plugin state,
// including this one.
//
// A plugin used to be registered on whatever host happened to load it, so
// Plugins/Reload/Unload could read one map. A plugin is now a DEFINITION
// ([Load] returns one), so its state lives with it, and the host finds it the
// way it finds everything else about its composition: by walking. That is the
// same move the op registry made — one walk, and the projections stop keeping
// private copies of what the program already says.
func (a *App) hosts() []*App {
	out := []*App{a}
	seen := map[*App]bool{a: true}
	for _, o := range a.plan() {
		if child, ok := o.app(); ok && !seen[child] {
			seen[child] = true
			out = append(out, child)
		}
	}
	return out
}

// pluginNamed finds the definition holding the named plugin, and the plugin.
func (a *App) pluginNamed(name string) (*App, *plugin) {
	for _, h := range a.hosts() {
		h.plugMu.Lock()
		p := h.plugins[name]
		h.plugMu.Unlock()
		if p != nil {
			return h, p
		}
	}
	return nil, nil
}
