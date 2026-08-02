package zip

import (
	"strings"
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
	if err := host.Drop(doomed); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if code, _ := wireGET(t, host, "/doomed"); code != 404 {
		t.Errorf("dropped route still answers %d", code)
	}
	if code, _ := wireGET(t, host, "/keep"); code != 200 {
		t.Errorf("Drop took an unrelated route with it: %d", code)
	}
}

// A Drop whose RESULT would not build must leave the live generation serving,
// exactly as a refused Include does. Here the drop removes the definition whose
// routes were the only thing making an included middleware-carrying group legal,
// so the resulting program is refused.
func TestTransactional_ARefusedDropChangesNothing(t *testing.T) {
	guarded := quiet("guarded")
	guarded.Use(H(func(c *Ctx) error { return c.Continue() }))
	routes := quiet("routes")
	routes.Get("/r", func(c *Ctx) error { return c.String(200, "r") })
	guarded.Use(routes) // the group is legal because routes live beneath it

	host := quiet("host")
	host.Use(guarded)
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	gen0, _ := host.Generation()

	// Dropping `routes` from `guarded` would leave middleware with nothing
	// beneath it — an inert guard, which the walk refuses.
	err := guarded.Drop(routes)
	if err == nil {
		t.Fatal("a Drop producing an invalid program was accepted")
	}
	if !strings.Contains(err.Error(), "no routes") {
		t.Errorf("refusal does not name the cause: %v", err)
	}
	if n, _ := host.Generation(); n != gen0 {
		t.Errorf("a refused Drop advanced the generation %d -> %d", gen0, n)
	}
	if code, _ := wireGET(t, host, "/r"); code != 200 {
		t.Errorf("a refused Drop disturbed the live generation: %d", code)
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
			_ = h1.Include(p)
		}(i)
		go func(n int) {
			defer wg.Done()
			p := quiet("q")
			p.Get("/q", func(c *Ctx) error { return nil })
			_ = h2.Include(p)
		}(i)
		go func(n int) { defer wg.Done(); _ = h1.Drop(quiet("nothing")) }(i)
	}
	go func() { wg.Wait(); close(done) }()
	<-done // a deadlock hangs here and the test times out, which is the assertion
}
