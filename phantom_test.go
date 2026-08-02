package zip

import (
	"strings"
	"sync"
	"testing"
)

// TestPhantomComposition_PostSealUseAlwaysPanics.
//
// The freeze used to carry an exemption: appendEntry allowed a write while a
// transaction was in flight. No transaction path reaches appendEntry — compose,
// Drop and includeRoutes all append directly, already holding buildMu — so the
// exemption could only ever admit a Use racing a transaction from ANOTHER
// goroutine.
//
// That append landed on the program and materialised on the next, unrelated
// Include: a plugin load in one subsystem could activate a route appended hours
// earlier by a caller that believed it had failed. Taking the lock made the
// phantom reliable rather than absent, which is the worse failure — the
// framework being silently wrong in the most literal way.
//
// Use is compose-time only. Include is the one transactional live verb. Two
// verbs, two lifetimes, neither silent.
func TestPhantomComposition_PostSealUseAlwaysPanics(t *testing.T) {
	host := quiet("host")
	host.Get("/live", func(c *Ctx) error { return c.String(200, "live") })
	if err := build(host); err != nil {
		t.Fatalf("build: %v", err)
	}

	ghost := quiet("ghost")
	ghost.Get("/phantom", func(c *Ctx) error { return c.String(200, "phantom") })

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("Use on a built app was accepted — the entry is a phantom, waiting for the next Include")
			}
			msg, _ := r.(string)
			for _, want := range []string{"frozen", `"host"`, "App.Include", "phantom_test.go:"} {
				if !strings.Contains(msg, want) {
					t.Errorf("panic does not mention %q: %s", want, msg)
				}
			}
		}()
		host.Use(ghost)
	}()

	// The refused Use left NOTHING behind: a later, unrelated Include must not
	// suddenly materialise the phantom route.
	later := quiet("later")
	later.Get("/later", func(c *Ctx) error { return c.String(200, "later") })
	if err := hostOf(t, host).Include(later); err != nil {
		t.Fatalf("Include: %v", err)
	}
	if code, _ := wireGET(t, host, "/phantom"); code != 404 {
		t.Errorf("an unrelated Include materialised the phantom route (%d) — the entry survived a refused Use", code)
	}
	if code, _ := wireGET(t, host, "/later"); code != 200 {
		t.Errorf("the legitimate Include did not take effect (%d)", code)
	}
}

// The same thing under contention, which is the shape the exemption existed for:
// a Use racing a transaction must panic, not slip through the window.
func TestPhantomComposition_UseRacingATransactionStillPanics(t *testing.T) {
	host := quiet("host")
	host.Get("/live", func(c *Ctx) error { return c.String(200, "live") })
	if err := build(host); err != nil {
		t.Fatalf("build: %v", err)
	}

	var wg sync.WaitGroup
	var panics, slipped int64
	var mu sync.Mutex
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					panics++
					mu.Unlock()
				}
			}()
			g := quiet("g")
			g.Get("/g", func(c *Ctx) error { return nil })
			host.Use(g) // must panic, every time
			mu.Lock()
			slipped++
			mu.Unlock()
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := quiet("p")
			p.Get("/p", func(c *Ctx) error { return nil })
			_ = hostOf(t, host).Include(p) // may fail on a collision; either is fine
		}()
	}
	wg.Wait()

	if slipped != 0 {
		t.Errorf("%d Use calls slipped past the freeze during a transaction — the phantom is back", slipped)
	}
	if panics != 16 {
		t.Errorf("only %d of 16 concurrent Use calls were refused", panics)
	}
}
