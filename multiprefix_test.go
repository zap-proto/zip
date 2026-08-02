package zip

import (
	"strings"
	"testing"
)

// The trap the fleet migration found: iam mounts on a SCOPE WITH 5 PREFIXES.
//
// A real identity provider owns several disjoint subtrees — /v1/iam,
// /login/oauth, /.well-known and friends — and a host that hands it a
// multi-prefix scope must not re-address the whole definition under every one
// of them. Composing an App once per prefix is composing it FIVE times, which
// is five occurrences of every route it owns.
//
// This pins what actually happens, so the migration has an answer rather than a
// guess.
func TestMultiPrefixScope_OneDefinitionPerPrefixIsFiveOccurrences(t *testing.T) {
	prefixes := []string{"/v1/iam", "/login/oauth", "/.well-known", "/oidc", "/scim"}

	iam := quiet("iam")
	iam.Get("/keys", func(c *Ctx) error { return c.String(200, "keys") })

	host := quiet("host")
	for _, p := range prefixes {
		host.Group(p).Use(iam)
	}
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	// Five prefixes, five distinct addresses. No collision, because each
	// occurrence answers under its own prefix.
	for _, p := range prefixes {
		if code, body := wireGET(t, host, p+"/keys"); code != 200 || body != "keys" {
			t.Errorf("%s/keys answered %d %q", p, code, body)
		}
	}
	// And the definition is still ONE definition.
	n := 0
	for _, def := range host.hostSet() {
		if def == iam {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the shared definition appears %d times in the host set, want 1", n)
	}
}

// The failure mode the trap actually is: a definition whose routes are ALREADY
// absolute cannot be re-addressed under a prefix without moving them. If a host
// composes an App under a prefix, the routes move — so a host that wants iam at
// its own absolute paths composes it at the ROOT, once, not once per prefix.
func TestMultiPrefixScope_ComposingAtRootKeepsAbsolutePaths(t *testing.T) {
	iam := quiet("iam")
	for _, p := range []string{"/v1/iam/keys", "/login/oauth/token", "/.well-known/jwks"} {
		iam.Get(p, func(c *Ctx) error { return c.String(200, "ok") })
	}

	host := quiet("host")
	host.Use(iam) // ONCE, at the root: the definition's paths are already absolute
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, p := range []string{"/v1/iam/keys", "/login/oauth/token", "/.well-known/jwks"} {
		if code, _ := wireGET(t, host, p); code != 200 {
			t.Errorf("%s answered %d — composing at the root must not move absolute paths", p, code)
		}
	}
}

// And the mistake, refused loudly: composing an absolute-path definition under
// five prefixes does NOT serve its absolute paths, it serves five prefixed
// copies. A migration that does this silently loses every original address.
func TestMultiPrefixScope_PrefixingAnAbsoluteDefinitionMovesIt(t *testing.T) {
	iam := quiet("iam")
	iam.Get("/v1/iam/keys", func(c *Ctx) error { return c.String(200, "keys") })

	host := quiet("host")
	host.Group("/v1/iam").Use(iam) // the mistake: prefix applied to absolute paths
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	if code, _ := wireGET(t, host, "/v1/iam/keys"); code != 404 {
		t.Errorf("/v1/iam/keys answered %d — expected the prefix to have MOVED it", code)
	}
	if code, _ := wireGET(t, host, "/v1/iam/v1/iam/keys"); code != 200 {
		t.Errorf("the doubled path does not answer either; the prefix did something else")
	}
	// The doubling is visible in the registry, which is where a migration should
	// notice it rather than in production.
	var paths []string
	for _, o := range host.plan() {
		if r, ok := o.route(); ok {
			paths = append(paths, o.abs(r.path))
		}
	}
	if !strings.Contains(strings.Join(paths, " "), "/v1/iam/v1/iam/keys") {
		t.Errorf("registry does not show the doubled path: %v", paths)
	}
}
