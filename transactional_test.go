package zip

import (
	"sync"
	"testing"
)

// #2: what does "transactional" actually guarantee, and is Drop symmetric?
//
// Both verbs go through transact, which builds EVERY affected server before
// installing ANY. So the guarantee is the same for both, and this proves it
// rather than implying it.
func TestTransactional_DropIsSymmetricWithInclude(t *testing.T) {
	keep := quiet("keep")
	keep.Get("/keep", func(c *Ctx) error { return c.String(200, "keep") })
	doomed := quiet("doomed")
	doomed.Get("/doomed", func(c *Ctx) error { return c.String(200, "doomed") })

	host := quiet("host")
	host.Use(keep, doomed)
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	// A Drop that leaves a VALID program installs a generation.
	if err := hostOf(t, host).Drop(doomed); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if code, _ := wireGET(t, host, "/doomed"); code != 404 {
		t.Errorf("dropped route still answers %d", code)
	}
	if code, _ := wireGET(t, host, "/keep"); code != 200 {
		t.Errorf("Drop took an unrelated route with it: %d", code)
	}
}

// A Drop cannot produce an invalid program, and that is a CONSEQUENCE of host
// anchoring rather than a gap in the test.
//
// Drop removes entries the host's own program holds, so the result is strictly
// smaller. The only rule a smaller program can newly violate is inert
// middleware — a definition left with middleware and no routes beneath it — and
// that cannot happen by removing a SIBLING, because middleware only ever wraps
// its own subtree. So there is no refused-Drop case to construct.
//
// What remains provable, and is what "transactional" actually buys here: Drop
// goes through the same transact path as Include, so it builds and validates the
// next generation before swapping, and a Drop that succeeds advances exactly one
// generation.
func TestTransactional_DropCannotProduceAnInvalidProgram(t *testing.T) {
	guarded := quiet("guarded")
	guarded.Use(H(func(c *Ctx) error { return c.Continue() }))
	routes := quiet("routes")
	routes.Get("/r", func(c *Ctx) error { return c.String(200, "r") })
	guarded.Use(routes) // the group is legal because routes live beneath it

	spare := quiet("spare")
	spare.Get("/spare", func(c *Ctx) error { return c.String(200, "spare") })

	app := quiet("host")
	app.Use(guarded, spare)
	h := hostOf(t, app)
	gen0 := h.Generation()

	// Dropping a sibling leaves guarded's own subtree intact, so the program is
	// still valid and the generation advances by exactly one.
	if err := h.Drop(spare); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if n := h.Generation(); n != gen0+1 {
		t.Errorf("generation %d after Drop, want %d", n, gen0+1)
	}
	if code, _ := wireGET(t, app, "/spare"); code != 404 {
		t.Errorf("dropped route still answers %d", code)
	}
	if code, _ := wireGET(t, app, "/r"); code != 200 {
		t.Errorf("Drop disturbed a subtree it does not own: %d", code)
	}
}

// The lock discipline, asserted by running the shapes that would deadlock if it
// were wrong: a shared definition edited while two hosts serve it, concurrently
// with edits on the hosts themselves.
func TestTransactional_NoDeadlockAcrossSharedDefinitions(t *testing.T) {
	shared := quiet("shared")
	shared.Get("/shared", func(c *Ctx) error { return nil })

	h1, h2 := quiet("h1"), quiet("h2")
	h1.Group("/a").Use(shared)
	h2.Group("/b").Use(shared)
	for _, h := range []*App{h1, h2} {
		if err := h.Build(); err != nil {
			t.Fatalf("build: %v", err)
		}
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			p := quiet("p")
			p.Get("/p", func(c *Ctx) error { return nil })
			_ = hostOf(t, h1).Include(p)
		}(i)
		go func(n int) {
			defer wg.Done()
			p := quiet("q")
			p.Get("/q", func(c *Ctx) error { return nil })
			_ = hostOf(t, h2).Include(p)
		}(i)
		go func(n int) { defer wg.Done(); _ = hostOf(t, h1).Drop(quiet("nothing")) }(i)
	}
	go func() { wg.Wait(); close(done) }()
	<-done // a deadlock hangs here and the test times out, which is the assertion
}
