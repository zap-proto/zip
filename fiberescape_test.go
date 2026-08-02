package zip

import (
	"context"

	fiber "github.com/zap-proto/fiber/v3"
	"strings"
	"testing"
)

// The footgun a consumer migration found, guarded.
//
// hanzoai/playground had s.Router.Fiber().Use(cors.New(...)). Fiber() returns
// the CURRENT generation's router, and a generation is a projection — so the
// next registration materialised a fresh one and the CORS middleware was
// silently discarded. No error, no panic, no CORS headers on any response.
func TestFiberEscape_ForeignRegistrationIsRefusedNotDiscarded(t *testing.T) {
	app := quiet("host")
	app.Get("/live", func(c *Ctx) error { return c.String(200, "live") })
	h, err := host2(app)
	if err != nil {
		t.Fatalf("host: %v", err)
	}

	// The mistake: registering on the value Fiber() handed out.
	app.Fiber().Get("/smuggled", func(fc fiber.Ctx) error { return fc.SendString("smuggled") })

	// The next composition must REFUSE rather than silently drop it.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a rebuild discarded a route registered on Fiber() without saying so")
		}
		msg, _ := r.(string)
		for _, want := range []string{"App.Fiber()", "discard", "app.Use(mw)"} {
			if !strings.Contains(msg, want) {
				t.Errorf("refusal does not mention %q: %s", want, msg)
			}
		}
	}()
	later := quiet("later")
	later.Get("/later", func(c *Ctx) error { return nil })
	_ = h.Include(later)
}

// The guard must not fire on zip's own work: control routes, plugin mounts and
// ordinary composition all rebuild without tripping it.
func TestFiberEscape_OrdinaryCompositionDoesNotTripTheGuard(t *testing.T) {
	app := quiet("host")
	Get(app, "/v1/thing", func(ctx1 context.Context, in *invoiceIn) (*invoiceOut, error) { return &invoiceOut{}, nil },
		WithOperationID("thing"))
	h, err := host2(app)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	for i := 0; i < 3; i++ {
		p := quiet("p" + itoa(i))
		p.Get("/p"+itoa(i), func(c *Ctx) error { return nil })
		if err := h.Include(p); err != nil {
			t.Fatalf("Include %d: %v", i, err)
		}
	}
	if n := h.Generation(); n != 3 {
		t.Errorf("generation = %d, want 3", n)
	}
}
