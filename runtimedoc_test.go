package zip

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// The runtime document makes claims; these check them against the code.

// "Before any host exists, Registry(app) recomputes per call and is not
// goroutine-safe — mutable phase, caller's problem."
func TestRuntimeDoc_RegistryRecomputesBeforeAHostExists(t *testing.T) {
	a := billingApp()
	first := a.Registry()
	if _, live := a.Generation(); live {
		t.Fatal("inspecting a program installed a generation")
	}
	// Recomputed, not memoised: a different backing array each call.
	if &first[0] == &a.Registry()[0] {
		t.Error("Registry() memoised before a host exists; the doc says it recomputes")
	}
	// And once a host exists it is the generation's, computed once.
	h, err := host2(a)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	if &h.Registry()[0] != &h.Registry()[0] {
		t.Error("Registry() is not stable on a live generation")
	}
}

// "Each generation retains its walk result; reducers serving the live
// generation reuse it rather than rewalking."
func TestRuntimeDoc_GenerationRetainsItsWalkResult(t *testing.T) {
	a := billingApp()
	h, err := host2(a)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	g := a.live.Load()
	if g == nil || len(g.occ) == 0 {
		t.Fatal("the live generation retains no walk result")
	}
	// plan() hands back that same slice rather than walking again.
	if &a.plan()[0] != &g.occ[0] {
		t.Error("plan() rewalked instead of reusing the generation's result")
	}
	_ = h
}

// "Requests pin their generation on arrival; no locks on the hot path."
// Concurrent readers of the live generation while it is being replaced.
func TestRuntimeDoc_NoLocksOnTheHotPath(t *testing.T) {
	a := quiet("host")
	a.Get("/x", func(c *Ctx) error { return c.String(200, "x") })
	h, err := host2(a)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = a.Registry(); _ = a.live.Load().serve }()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p := quiet("p")
		p.Get("/p", func(c *Ctx) error { return nil })
		_ = h.Include(p)
	}()
	wg.Wait()
}

// "Registry and Declaration panic with the joined validation error when the
// program does not compose — validation runs before any rendering, so no tool
// can emit an empty document and exit 0."
func TestRuntimeDoc_ProjectionsPanicRatherThanEmit(t *testing.T) {
	a := quiet("broken")
	a.Get("/dup", func(c *Ctx) error { return nil })
	a.Get("/dup", func(c *Ctx) error { return nil })

	for name, fn := range map[string]func(){
		"Registry":    func() { _ = a.Registry() },
		"Declaration": func() { _ = a.Declaration() },
	} {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("%s() answered for a program that does not compose", name)
					return
				}
				if msg, _ := r.(string); !strings.Contains(msg, "/dup") {
					t.Errorf("%s() panic does not carry the validation error: %v", name, r)
				}
			}()
			fn()
		}()
	}
}

// Derived validation is its own stage: two occurrences can be structurally fine
// and still derive one operation id, which does not exist until derivation runs.
func TestRuntimeDoc_DerivedValidationCatchesIDCollisions(t *testing.T) {
	// Two DIFFERENT definitions, different paths (no address conflict), both
	// declaring the same operation id, composed at the same prefix.
	one := quiet("one")
	Get(one, "/a", func(ctx1 context.Context, in *invoiceIn) (*invoiceOut, error) { return &invoiceOut{}, nil },
		WithOperationID("shared"))
	two := quiet("two")
	Get(two, "/b", func(ctx1 context.Context, in *invoiceIn) (*invoiceOut, error) { return &invoiceOut{}, nil },
		WithOperationID("shared"))

	host := quiet("host")
	host.Use(one, two)

	occ, err := walk(host)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if err := structural(occ); err != nil {
		t.Fatalf("structural validation should pass — the addresses do not collide: %v", err)
	}
	err = derived(occ)
	if err == nil {
		t.Fatal("two occurrences derived one operation id and derived validation passed")
	}
	if !strings.Contains(err.Error(), "shared") {
		t.Errorf("refusal does not name the id: %v", err)
	}
}
