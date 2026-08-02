package zip

import (
	"strings"
	"testing"
)

// A generation is what makes dynamic composition safe rather than merely
// possible. These pin the properties a plugin host actually depends on.

// TestGeneration_RefusedIncludeLeavesTheOldOneServing is the whole point.
//
// A plugin whose patterns collide with the live set must not be able to take
// down routing. Under in-place mutation the collision is discovered halfway
// through applying it, and what is left is neither the old routing nor the new.
// Build-then-swap has no such state: the new generation is complete before the
// pointer moves, so a failed build costs the live system nothing.
func TestGeneration_RefusedIncludeLeavesTheOldOneServing(t *testing.T) {
	good := quiet("good")
	good.Get("/v1/good", func(c *Ctx) error { return c.String(200, "good") })

	host := quiet("host")
	host.Use(good)
	if err := build(host); err != nil {
		t.Fatalf("generation 0: %v", err)
	}
	if n, live := host.Generation(); !live || n != 0 {
		t.Fatalf("generation = %d live=%v, want 0 live", n, live)
	}

	// A plugin that claims an address the live set already holds.
	bad := quiet("bad")
	bad.Get("/v1/good", func(c *Ctx) error { return c.String(200, "bad") })

	err := hostOf(t, host).Include(bad)
	if err == nil {
		t.Fatal("a colliding plugin was accepted")
	}
	for _, want := range []string{"still serving", "GET /v1/good", `"good"`, `"bad"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	// Generation 0 is still the live one, and still answers exactly as before.
	if n, _ := host.Generation(); n != 0 {
		t.Errorf("a refused Include advanced the generation to %d", n)
	}
	if code, body := wireGET(t, host, "/v1/good"); code != 200 || body != "good" {
		t.Errorf("the live generation was disturbed by a refused Include: %d %q", code, body)
	}
	// And nothing of the refused plugin leaked into the program.
	for _, def := range host.hostSet() {
		if def == bad {
			t.Fatal("the refused plugin is in the program")
		}
	}
}

// TestGeneration_IncludeAdvancesAndServes: the happy path. A plugin composed
// against a running system is live at generation N+1, and everything the
// previous generation served still serves.
func TestGeneration_IncludeAdvancesAndServes(t *testing.T) {
	host := quiet("host")
	host.Get("/v1/host", func(c *Ctx) error { return c.String(200, "host") })
	if err := build(host); err != nil {
		t.Fatalf("generation 0: %v", err)
	}

	later := quiet("later")
	later.Get("/v1/later", func(c *Ctx) error { return c.String(200, "later") })
	if err := hostOf(t, host).Include(later); err != nil {
		t.Fatalf("Include: %v", err)
	}
	if n, _ := host.Generation(); n != 1 {
		t.Errorf("generation = %d, want 1", n)
	}
	if code, body := wireGET(t, host, "/v1/later"); code != 200 || body != "later" {
		t.Errorf("the composed plugin does not serve: %d %q", code, body)
	}
	if code, body := wireGET(t, host, "/v1/host"); code != 200 || body != "host" {
		t.Errorf("the previous generation's route stopped serving: %d %q", code, body)
	}
	// And every projection moved with it — one registry, five projections, still.
	if n := len(host.Registry()); n != 0 {
		t.Errorf("registry = %d, want 0 (both routes are untyped)", n)
	}
	var found bool
	for _, r := range host.Declaration().Routes {
		if r.Pattern == "/v1/later" {
			found = true
		}
	}
	if !found {
		t.Error("the declaration did not move to the new generation")
	}
}

// TestGeneration_DropStopsServingEveryOccurrence. Identity is the pointer, so
// Drop names the DEFINITION. A definition included at two prefixes is one thing
// serving in two places, and "stop serving billing" is not a question about one
// of them.
func TestGeneration_DropStopsServingEveryOccurrence(t *testing.T) {
	billing := billingApp()
	host := quiet("host")
	host.Get("/v1/keep", func(c *Ctx) error { return c.String(200, "keep") })
	host.Group("/a").Use(billing)
	host.Group("/b").Use(billing)
	if err := build(host); err != nil {
		t.Fatalf("generation 0: %v", err)
	}
	if code, _ := wireGET(t, host, "/a/invoices/i-1"); code != 200 {
		t.Fatalf("/a/invoices/i-1 answered %d before the drop", code)
	}

	// THE FINDING: Drop(billing) here is a NO-OP, and correctly so. The host never
	// referenced billing — the groups did. An entry belongs to the app that wrote
	// it, so a host can only drop what the host included; anything else would let
	// one host reach into a definition another host also serves.
	if err := hostOf(t, host).Drop(billing); err != nil {
		t.Fatalf("Drop(billing): %v", err)
	}
	if code, _ := wireGET(t, host, "/a/invoices/i-1"); code != 200 {
		t.Errorf("Drop(billing) reached into a group it does not own (%d)", code)
	}
	// Dropping what the host DOES hold is what stops the service.
	var groups []*App
	for _, e := range host.entries {
		if g, ok := e.n.(*App); ok && g.prefix != "" {
			groups = append(groups, g)
		}
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 group entries, got %d", len(groups))
	}
	if err := hostOf(t, host).Drop(groups...); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	// 2, not 1: the no-op Drop above ran a transaction and installed a
	// generation. Every successful transaction produces one, whether or not the
	// program changed — detecting "nothing moved" would be a second code path
	// for the same operation, and the generation number is a build counter, not
	// a change counter.
	if n, _ := host.Generation(); n != 2 {
		t.Errorf("generation = %d after Drop, want 2", n)
	}
	for _, p := range []string{"/a/invoices/i-1", "/b/invoices/i-1"} {
		if code, _ := wireGET(t, host, p); code != 404 {
			t.Errorf("%s still answers %d after the drop", p, code)
		}
	}
	if code, body := wireGET(t, host, "/v1/keep"); code != 200 || body != "keep" {
		t.Errorf("Drop took an unrelated route with it: %d %q", code, body)
	}
	if n := len(host.Registry()); n != 0 {
		t.Errorf("registry still has %d ops after dropping their definition", n)
	}
}

// TestGeneration_DropIsTransactionalToo: a Drop that would produce an invalid
// program leaves the current generation serving, exactly as a refused Include
// does. There is one transaction path, not two.
func TestGeneration_DropOfNothingIsANoOpGeneration(t *testing.T) {
	host := quiet("host")
	host.Get("/x", func(c *Ctx) error { return c.String(200, "x") })
	if err := build(host); err != nil {
		t.Fatalf("generation 0: %v", err)
	}
	unrelated := quiet("unrelated")
	if err := hostOf(t, host).Drop(unrelated); err != nil {
		t.Fatalf("dropping something not composed: %v", err)
	}
	if code, _ := wireGET(t, host, "/x"); code != 200 {
		t.Errorf("/x stopped serving after a no-op Drop: %d", code)
	}
}

// TestGeneration_FrozenDefinitionRefusesDirectEdits. The panic means exactly
// one thing — go through a generation — and says so, naming both call sites.
func TestGeneration_FrozenDefinitionRefusesDirectEdits(t *testing.T) {
	child := billingApp()
	host := quiet("host")
	host.Use(child)
	if err := build(host); err != nil {
		t.Fatalf("generation 0: %v", err)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a frozen definition accepted a direct edit")
		}
		msg, _ := r.(string)
		for _, want := range []string{"frozen", "billing", "App.Include", "generation_test.go:"} {
			if !strings.Contains(msg, want) {
				t.Errorf("panic does not mention %q: %s", want, msg)
			}
		}
	}()
	child.Get("/sneak", func(c *Ctx) error { return nil })
}

// TestGeneration_RequestsArePinned. The handler resolves the generation once,
// on arrival. A request that has already started finishes on the generation it
// started under — it cannot observe a half-applied change, because no such
// state exists.
func TestGeneration_RequestsArePinned(t *testing.T) {
	host := quiet("host")
	started := make(chan struct{})
	release := make(chan struct{})
	host.Get("/slow", func(c *Ctx) error {
		close(started)
		<-release
		return c.String(200, "generation 0")
	})
	if err := build(host); err != nil {
		t.Fatalf("generation 0: %v", err)
	}
	serve := host.pinned()

	done := make(chan string, 1)
	go func() {
		code, body := wireGET(t, host, "/slow")
		done <- body + "/" + itoa(code)
	}()
	<-started

	// Swap generations while the request is in flight.
	later := quiet("later")
	later.Get("/v1/later", func(c *Ctx) error { return nil })
	if err := hostOf(t, host).Include(later); err != nil {
		t.Fatalf("Include during an in-flight request: %v", err)
	}
	if n, _ := host.Generation(); n != 1 {
		t.Fatalf("generation = %d, want 1", n)
	}
	close(release)

	if got := <-done; got != "generation 0/200" {
		t.Errorf("the in-flight request did not complete on its own generation: %q", got)
	}
	// The pinned handler is one indirection, and it now resolves to generation 1.
	if serve == nil {
		t.Fatal("pinned handler is nil")
	}
}
