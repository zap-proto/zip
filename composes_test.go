package zip

import (
	"strings"
	"testing"
)

// A host composes each plugin as its own definition and asks it for its routes
// before composing it. Verify is how it learns whether that definition holds
// together. Asking must not end the definition's life: Declaration panics when
// the program does not compose, and Build installs a generation and freezes —
// and the host is about to compose this definition by reference.

func TestVerify_SaysWhatBuildWouldSay(t *testing.T) {
	// A guard composed into the plugin, with nothing beneath it to guard.
	plugin := func() *App {
		guard := quiet("guard")
		guard.Use(H(func(c *Ctx) error { return c.Continue() }))

		p := quiet("plugin")
		p.Raw("GET", "/health", func(c *Ctx) error { return nil })
		p.Use(guard)
		return p
	}

	answer, verdict := plugin().Verify(), plugin().Build()
	if answer == nil || verdict == nil {
		t.Fatalf("middleware guarding no routes: Verify=%v Build=%v — both must refuse", answer, verdict)
	}
	if answer.Error() != verdict.Error() {
		t.Fatalf("Verify and Build disagree about the same definition\n Verify: %s\n  Build: %s", answer, verdict)
	}
	if !strings.Contains(answer.Error(), "no routes anywhere beneath it") {
		t.Fatalf("the verdict should name the rule it broke, got: %s", answer)
	}
}

func TestVerify_LeavesTheDefinitionComposable(t *testing.T) {
	def := quiet("plugin")
	def.Raw("GET", "/health", func(c *Ctx) error { return nil })

	if err := def.Verify(); err != nil {
		t.Fatalf("a definition with a route under no middleware: %v", err)
	}
	if def.Frozen() {
		t.Fatal("Verify froze the definition — it renders no projection, so there is nothing to protect")
	}

	// Still writable, and still composable by reference afterwards.
	def.Raw("GET", "/ready", func(c *Ctx) error { return nil })

	host := quiet("host")
	host.Use(def)
	if err := host.Build(); err != nil {
		t.Fatalf("host composing a verified definition: %v", err)
	}
}

func TestVerify_IsTheHalfThatBuildFreezesAfter(t *testing.T) {
	def := quiet("plugin")
	def.Raw("GET", "/health", func(c *Ctx) error { return nil })

	if err := def.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !def.Frozen() {
		t.Fatal("Build did not freeze — then Verify is not distinguishable from it and should not exist")
	}
}

// Verify answers for the definition as the root of its own walk — as if it were
// the app being served. One rule reads differently from there: middleware at a
// served root wraps the routes composition brings it later, so it is exempt,
// while the same middleware one level down guards a subtree that is already
// settled. Only the host knows which of the two a definition turned out to be,
// so that verdict stays with the host's own Build.
func TestVerify_AnswersForTheDefinitionAsServed(t *testing.T) {
	def := quiet("plugin")
	def.Use(H(func(c *Ctx) error { return c.Continue() }))

	if err := def.Verify(); err != nil {
		t.Fatalf("middleware at a served root is exempt, so standalone this composes: %v", err)
	}

	host := quiet("host")
	host.Raw("GET", "/health", func(c *Ctx) error { return nil })
	host.Use(def)
	if err := host.Build(); err == nil {
		t.Fatal("composed one level down the same middleware guards nothing: the host must refuse")
	}
}
