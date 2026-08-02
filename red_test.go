package zip

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Adversarial tests for the one-verb fold. Each one FAILS on the branch as
// written and names the single line that causes it. Nothing here is a style
// complaint: every assertion is a runtime behaviour a caller can observe.

func redOK(c *Ctx) error { return c.String(200, "ok") }

func redGuard(next Handler) Handler {
	return func(c *Ctx) error {
		if c.Header("Authorization") == "" {
			return Errorf(401, "no credentials")
		}
		return next(c)
	}
}

// ---------------------------------------------------------------------------
// 1. CONCURRENCY / STALENESS
// ---------------------------------------------------------------------------

// TestRed_DropNeverMovesTheVersionCounter.
//
// liveOrBuild caches a draft keyed on the process-wide `version` counter. Every
// other mutation moves that counter (appendEntry, compose, includeRoutes, and
// even transact's own rollback path). App.Drop is the one that does not — it
// assigns a.entries = kept inside the transaction and never calls version.Add.
//
// So a definition that is Drop'ed keeps serving, forever, in every OTHER app
// that had already materialised a draft over it. The host is not frozen and has
// no live generation, so nothing ever forces it to rebuild.
func TestRed_DropNeverMovesTheVersionCounter(t *testing.T) {
	child := quiet("child")
	child.Get("/keep", redOK)
	sub := child.Group("/sub").(*App) // Drop takes the concrete child
	sub.Get("/gone", redOK)

	host := quiet("host")
	host.Use(child)

	// Ordinary pre-Listen inspection: a codegen step, a doc generator, Fiber().
	// It materialises a draft and CACHES it against the version counter.
	if code, _ := wireGET(t, host, "/sub/gone"); code != 200 {
		t.Fatalf("setup: host does not serve /sub/gone: %d", code)
	}
	before := version.Load()

	if err := hostOf(t, child).Drop(sub); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	if after := version.Load(); after != before {
		t.Logf("version moved %d -> %d (bug is fixed)", before, after)
	} else {
		t.Errorf("Drop did not move the version counter (still %d) — "+
			"generation.go:148 App.Drop assigns a.entries without version.Add(1)", before)
	}

	// The child itself is correct: it installed a generation, so it reads live.
	if code, _ := wireGET(t, child, "/sub/gone"); code == 200 {
		t.Errorf("the child still serves its own dropped route")
	}
	// The host is not. It answers out of a draft the Drop invalidated but never
	// marked stale.
	if code, _ := wireGET(t, host, "/sub/gone"); code == 200 {
		t.Errorf("a DROPPED route is still served by a host that composed it: "+
			"GET /sub/gone = %d (stale draft, draftAt == version)", code)
	}
}

// TestRed_TheSealIsNotEnforcedUnderConcurrency.
//
// appendEntry decides whether to panic with `a.frozen.Load() && !a.building`.
// frozen is atomic; building is a plain bool, written by transact under buildMu
// and read here under no lock at all. Include is BY DESIGN called against a
// running system, so a second goroutine reaching Use is exactly the case the
// seal exists to catch — and the check that catches it is racy.
//
// When the read observes building==true the panic is skipped and the goroutine
// appends to a.entries while transact is copying and reassigning it: a torn
// entry list, or a silently discarded registration.
//
// Run with -race; the detector is the assertion.
func TestRed_TheSealIsNotEnforcedUnderConcurrency(t *testing.T) {
	host := quiet("host")
	host.Get("/ok", redOK)
	if err := build(host); err != nil {
		t.Fatalf("generation 0: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	// The control plane: composing against a running system, the sanctioned way.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			c := quiet("c")
			c.Get("/red/"+itoa(i), redOK)
			_ = hostOf(t, host).Include(c)
		}
	}()
	// Anything else that reaches Use. The seal's whole job is to refuse this
	// with a panic naming the freeze site.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			func() {
				defer func() { _ = recover() }()
				host.Use(H(redOK))
			}()
		}
	}()
	wg.Wait()
}

// ---------------------------------------------------------------------------
// 2. TRANSACTION ROLLBACK IS NOT TOTAL
// ---------------------------------------------------------------------------

// TestRed_RefusedIncludeStillAdoptsTheChildsTeardown.
//
// transact restores a.entries and nothing else. compose() has a SECOND effect —
// a.OnShutdown(v.ShutdownWithContext) — which is appended to a.hooks before the
// build runs and is never undone. Include's contract is "on error — changes
// nothing at all". It changes the host's teardown set.
//
// Consequence: a host that refuses a plugin still owns that plugin's shutdown,
// so shutting the host down stops a subsystem it never composed and which may
// still be serving somewhere else.
func TestRed_RefusedIncludeStillAdoptsTheChildsTeardown(t *testing.T) {
	host := quiet("host")
	host.Get("/v1/x", redOK)
	if err := build(host); err != nil {
		t.Fatalf("generation 0: %v", err)
	}

	bad := quiet("bad")
	bad.Get("/v1/x", redOK) // collides with the live set: Include must refuse
	var torn atomic.Bool
	bad.OnShutdown(func(context.Context) error { torn.Store(true); return nil })

	if err := hostOf(t, host).Include(bad); err == nil {
		t.Fatal("setup: the colliding definition was accepted")
	}

	if err := host.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if torn.Load() {
		t.Error("a REFUSED Include still adopted the child's teardown — " +
			"generation.go:212 compose() calls OnShutdown, generation.go:182 rolls back only a.entries")
	}
}

// TestRed_APanicInMaterialiseBricksTheProgram.
//
// A route whose method fiber does not know panics inside register(). That panic
// unwinds through build() and through transact() — past line 182, where the
// rollback lives. buildMu is released by defer, but a.entries keeps the edit.
//
// The live generation survives (requests keep being served), so the failure is
// invisible until the NEXT composition, which panics on the poison left behind.
// "A plugin whose patterns collide with the live set fails the build, with
// breadcrumbs, and the old generation keeps serving" holds for collisions and
// not for this.
func TestRed_AnUnknownMethodIsRefusedAtTheBoundary(t *testing.T) {
	// FIXED. PURGE is a real method (Varnish, Fastly, nginx cache invalidation),
	// so are PROPFIND and MKCOL, and a Declaration is a BUILD INPUT — JSON from
	// another team. Any of them used to reach fiber's register() as a PANIC,
	// which unwound past transact's rollback and left the poison in a.entries,
	// so the next entirely valid Include panicked too.
	//
	// Two fixes, both needed: the rollback is deferred (so a panic anywhere in
	// the build still restores the program), and the method is validated where
	// the input crosses the boundary (so it is an error, like every other bad
	// declaration, instead of a panic).
	host := quiet("host")
	_, err := remoteApp(host, "/v1/cdn", "http://127.0.0.1:9", Declaration{
		Name:   "cdn",
		Routes: []Route{{Method: "PURGE", Pattern: "/v1/cdn/object"}},
	})
	if err == nil {
		t.Fatal("an unroutable method was accepted from a Declaration")
	}
	for _, want := range []string{"PURGE", "/v1/cdn/object"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
	// And the host is not poisoned: a later valid composition still builds.
	ok := quiet("ok")
	ok.Get("/fine", redOK)
	host.Use(ok)
	if err := build(host); err != nil {
		t.Fatalf("the host was bricked by a refused declaration: %v", err)
	}
}

func TestRed_MethodCaseIsNormalisedBeforeTheConflictCheck(t *testing.T) {
	host := quiet("host")
	host.Get("/v1/thing", func(c *Ctx) error { return c.String(200, "host") })

	remote, err := remoteApp(host, "/v1", "http://127.0.0.1:9", Declaration{
		Name:   "remote",
		Routes: []Route{{Method: "get", Pattern: "/v1/thing"}},
	})
	if err != nil {
		t.Fatalf("remoteApp: %v", err)
	}
	host.Use(remote)

	// FIXED. fiber upper-cases the method inside register(), so a hand-written
	// Declaration saying "get" collided at runtime with the host's Get while
	// looking like a different key to the walk. conflicts() now upper-cases
	// before comparing. (The program does not build, so nothing may be asked of
	// its projections — see TestRed_ABuildErrorPanicsRatherThanEmptying...)
	err = build(host)
	if err == nil {
		t.Fatal(`"get" and "GET" claimed one address and the walk reported no conflict`)
	}
	for _, want := range []string{"GET /v1/thing", `"host"`, `"remote"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %s: %v", want, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. SILENT DROPS
// ---------------------------------------------------------------------------

// TestRed_ABuildErrorSilentlyEmptiesEveryProjection.
//
// liveOrBuild does `g, _ := a.build()` and substitutes an EMPTY generation when
// the build fails. build() fails on any conflict, so one duplicated address
// turns Registry, Fiber, Declaration, OpenAPISpec and the CLI into empty
// answers — with no error anywhere. Only Listen reports it.
//
// The worst reachable form is `app.Described()`: a release pipeline writes a
// declaration/OpenAPI file describing ZERO routes and exits 0.
func TestRed_ABuildErrorPanicsRatherThanEmptyingEveryProjection(t *testing.T) {
	// FIXED. One duplicated address used to turn Registry, Fiber, Declaration,
	// OpenAPISpec and the CLI into EMPTY answers with no error anywhere — and
	// the worst reachable form was `app declare` writing {"routes":[]} and
	// exiting 0, so a release pipeline shipped an empty API document for a
	// broken service and every downstream projection reproduced the emptiness.
	//
	// Registry and Declaration return values, not (value, error), so a program
	// that does not compose has no honest answer for them. They panic, carrying
	// the joined error. Same category as mutating a frozen App: you cannot
	// obtain a projection of a program that does not compose. A sentinel loses
	// here precisely because a sentinel can be ignored, and a caller ignoring it
	// is the failure this exists to stop.
	a := billingApp() // one typed op: GET /invoices/:id
	a.Get("/healthz", redOK)
	a.Get("/healthz", redOK) // the whole defect needed exactly this one line

	if err := build(a); err == nil {
		t.Fatal("setup: the duplicate was not detected at all")
	} else if !strings.Contains(err.Error(), "/healthz") {
		t.Fatalf("setup: unexpected build error: %v", err)
	}

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("Registry() answered for a program that does not compose")
			}
			if msg, _ := r.(string); !strings.Contains(msg, "/healthz") {
				t.Errorf("the panic does not carry the build error: %v", r)
			}
		}()
		_ = a.Registry()
	}()

	// And the projection a release pipeline consumes writes NOTHING and fails.
	dest := filepath.Join(t.TempDir(), "declare.json")
	err := a.project(Declare, dest)
	if err == nil {
		t.Fatal("`app declare` wrote a document for a program that does not build")
	}
	if !strings.Contains(err.Error(), "does not build") {
		t.Errorf("refusal does not say why: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("a refused projection still created %s", dest)
	}
}

// TestRed_AllShadowsAConcreteMethodAndTheWalkSaysNothing.
//
// conflicts() keys on addrKey(r.method, path) and methodAll is stored as the
// literal "ALL", so "ALL /v1/thing" and "GET /v1/thing" are two different keys.
// They are ONE address at runtime: whichever fiber registers first answers, and
// the other definition's route is dead code.
//
// This is load-bearing now that HEAD deleted App.claim: every mount and every
// plugin registers its prefix as methodAll, and the commit that removed the
// claims ledger asserts the walk "answers it better".
func TestRed_AllCollidesWithAConcreteMethod(t *testing.T) {
	// FIXED. methodAll is stored as the literal "ALL", so "ALL /v1/thing" and
	// "GET /v1/thing" were two keys to the walk and ONE address at runtime:
	// whichever fiber registered first answered, and the other definition's
	// route was dead code.
	//
	// Load-bearing, because deleting App.claim was justified on the walk
	// answering better — and the ledger's strongest case was mounts and plugin
	// prefixes, every one of which registers as methodAll.
	gateway := quiet("gateway")
	gateway.All("/v1/thing", func(c *Ctx) error { return c.String(200, "gateway") })

	owner := quiet("owner")
	owner.Get("/v1/thing", func(c *Ctx) error { return c.String(200, "owner") })

	host := quiet("host")
	host.Use(gateway, owner)

	err := build(host)
	if err == nil {
		t.Fatal("an All and a Get claimed one address and the walk reported no conflict")
	}
	for _, want := range []string{"/v1/thing", `"gateway"`, `"owner"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %s: %v", want, err)
		}
	}
}

func TestRed_AMiddlewareOnlyDefinitionIsRefusedAtSeal(t *testing.T) {
	// FIXED, by refusing rather than by bending the scoping rule.
	//
	// Under lexical anchoring a definition's middleware wraps THAT definition's
	// routes; with no routes there is nothing to wrap, so inert is CORRECT. The
	// defect was that it was silent and typographically identical to the blessed
	// pattern — app.Use(securityModule) reads exactly like working code and did
	// nothing at all, and Lint() said nothing either.
	//
	// Letting the middleware escape upward would have broken §4 for every other
	// definition, so the composition is refused instead.
	sec := quiet("sec")
	sec.Use(H(redGuard(redOK))) // a guard, and nothing to guard

	host := quiet("host")
	host.Get("/private", redOK)
	host.Use(sec)

	err := build(host)
	if err == nil {
		t.Fatal("a middleware-only definition was composed, and its guard silently never ran")
	}
	for _, want := range []string{`"sec"`, "no routes", "red_test.go:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}

	// The correct spelling still works: give the definition the routes it guards.
	ok := quiet("ok")
	ok.Use(H(redGuard(redOK)))
	ok.Get("/guarded", redOK)
	host2 := quiet("host2")
	host2.Use(ok)
	if err := build(host2); err != nil {
		t.Fatalf("a definition with middleware AND routes was refused: %v", err)
	}
}

func TestRed_IncludeIsAHostVerbSoTheNoOpIsUnaskable(t *testing.T) {
	// FIXED, by moving the verb rather than by fixing the reach.
	//
	// Include used to live on *App. Called on a GROUP it returned nil and changed
	// nothing a host served, because transact installed onto the receiver and a
	// group has no listener — while the freeze panic RECOMMENDED Include. The
	// first fix answered "which hosts does this reach?" with a process-level
	// server registry, reachability filtering and cross-host locks.
	//
	// Anchoring the verb to the host makes the question unaskable: there is no
	// App.Include to call on a group, and host.Include edits the host's own
	// program. All that machinery is gone.
	host := quiet("host")
	host.Get("/live", redOK)
	v1 := host.Group("/v1") // Router now, not *App — the abstraction survives
	v1.Get("/x", redOK)

	h, err := host2(host)
	if err != nil {
		t.Fatalf("host: %v", err)
	}

	later := quiet("later")
	later.Get("/later", redOK)
	if err := h.Include(later); err != nil {
		t.Fatalf("Include: %v", err)
	}
	// It took effect — no silent no-op anywhere.
	if code, _ := wireGET(t, host, "/later"); code != 200 {
		t.Errorf("host.Include did not take effect: %d", code)
	}
	// And the group is still frozen: composing into it panics and points at the
	// host verb, which now actually works.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a frozen group accepted a direct Use")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "frozen") {
			t.Errorf("panic does not name the freeze: %v", r)
		}
	}()
	v1.Get("/sneak", redOK)
}

func TestRed_ANilHandlerIsAcceptedAtBoot(t *testing.T) {
	app := quiet("host")
	var uninitialised Handler // e.g. a package-level middleware var, never set

	accepted := false
	func() {
		defer func() { _ = recover() }()
		app.Get("/x", uninitialised)
		accepted = true
	}()

	if accepted {
		t.Errorf(`app.Get("/x", nil) was accepted at registration — router.go:95 promises a ` +
			`boot panic, but toFiberHandler wraps the nil into a non-nil closure and the ` +
			`first request segfaults on a fasthttp goroutine no recover middleware can reach`)
	}
}

// ---------------------------------------------------------------------------
// 4. SCOPED MIDDLEWARE IS LOST (auth bypass)
// ---------------------------------------------------------------------------

// TestRed_AScopedWithIsLostByANestedGroup.
//
// On origin/main wrapRouter.Group returned another *wrapRouter, so the chain
// propagated to every nested group. On this branch Router.Group returns *App
// and wrapRouter.Group stashes the chain in g.wrap — but App.Group builds the
// child from groupConfig() and never copies a.wrap, so the chain stops at one
// level.
//
// A regression, and the failure mode is an ungated route.
func TestRed_AScopedWithIsLostByANestedGroup(t *testing.T) {
	app := quiet("host")
	v1 := app.With(redGuard).Group("/v1")
	v1.Get("/direct", redOK)
	sub := v1.Group("/sub")
	sub.Get("/nested", redOK)

	if err := build(app); err != nil {
		t.Fatalf("build: %v", err)
	}
	if code, _ := wireGET(t, app, "/v1/direct"); code != 401 {
		t.Fatalf("setup: the direct leaf is not gated either: %d", code)
	}
	if code, _ := wireGET(t, app, "/v1/sub/nested"); code != 401 {
		t.Errorf("a nested group escaped the scoped With: GET /v1/sub/nested = %d "+
			"(compose.go:254 newApp(a.groupConfig()) drops a.wrap)", code)
	}
}

// TestRed_WithUseDropsTheGateForTheWholeSubtree.
//
// wrapRouter implements Router, Router now carries the ONE composition verb, and
// wrapRouter.Use delegates straight to the inner App. Group propagates the
// chain and OpScope propagates the chain — with the comment that dropping it
// "is a hole, not an inconvenience" — and Use silently drops it.
func TestRed_WithUseRefusesToComposeADefinitionUngated(t *testing.T) {
	// FIXED. wrapRouter.Group propagates the chain and OpScope propagates the
	// chain — with a comment saying dropping it "is a hole, not an
	// inconvenience" — while Use silently dropped it. Now that Use is THE
	// composition verb, that was the widest of the three doors.
	//
	// It cannot wrap the definition's leaves (they belong to the definition, and
	// other hosts may compose the same one), so it refuses and says what to do
	// instead. Silence was the only unacceptable answer.
	child := quiet("child")
	child.Get("/child/x", redOK)

	app := quiet("host")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("With(...).Use(definition) composed it ungated")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "Group") {
			t.Errorf("refusal does not say how to scope the chain: %v", r)
		}
	}()
	app.With(redGuard).Use(child)
}

func TestRed_CleanDescendAncestorAliasing(t *testing.T) {
	shared := quiet("shared")
	shared.Get("/shared", redOK)

	left := quiet("left")
	left.Group("/l").Use(shared)
	right := quiet("right")
	right.Group("/r").Use(shared)

	root := quiet("root")
	root.Use(left, right)

	if err := verify(root); err != nil {
		t.Fatalf("diamond composition reported an error: %v", err)
	}
	n := 0
	occ, _ := walk(root)
	for _, o := range occ {
		if r, ok := o.route(); ok && strings.HasSuffix(r.path, "/shared") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("one definition reached by two paths yielded %d occurrences, want 2", n)
	}
}

// TestRed_CleanComposeOpsDoesNotEditTheDefinition: a definition's own document
// must be unchanged by being composed, at any number of prefixes.
func TestRed_CleanComposeOpsDoesNotEditTheDefinition(t *testing.T) {
	billing := billingApp()
	own := billing.Registry()
	if len(own) != 1 {
		t.Fatalf("setup: %d ops", len(own))
	}
	path, id, origin, tags := own[0].Path, own[0].OperationID, own[0].Origin, len(own[0].Tags)

	host := quiet("host")
	host.Group("/v1").Use(billing)
	host.Group("/admin").Use(billing)
	if got := len(host.Registry()); got != 2 {
		t.Fatalf("composed registry has %d ops, want 2", got)
	}
	if own[0].Path != path || own[0].OperationID != id || own[0].Origin != origin || len(own[0].Tags) != tags {
		t.Errorf("composing edited the definition's own op: %+v", own[0])
	}
}
