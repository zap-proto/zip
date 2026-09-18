package zip

import "testing"

// A raw route records no registrar — it has a method and a path and nothing
// else — so the only way to ask for its prose is by address. cmd/zipdoc files
// every entry under the package that declared it, raw routes included, so the
// address lookup has to reach a qualified key or a whole service's untyped
// half documents as silent.
func TestProse_ReachesAQualifiedKey(t *testing.T) {
	Describe(DocKey("github.com/hanzoai/iam/internal/oidc", "GET", "/v1/iam/whoami"),
		Doc{Description: "Whoami says which subject the presented credential names."})

	summary, description, ok := Prose("GET", "/v1/iam/whoami")
	if !ok {
		t.Fatal("a raw route's prose is unreachable by address, so every untyped route documents as undocumented")
	}
	if summary != "Whoami says which subject the presented credential names." {
		t.Fatalf("summary = %q", summary)
	}
	if description == "" {
		t.Fatal("no description")
	}
}

// Two packages describing one address is a conflict, not a choice: in one
// process one address answers once. Answering with either one would be a guess,
// so the address lookup declines and the qualified one still works.
func TestProse_DeclinesAnAddressTwoPackagesClaim(t *testing.T) {
	Describe(DocKey("a/one", "GET", "/v1/contested"), Doc{Description: "One says this."})
	Describe(DocKey("b/two", "GET", "/v1/contested"), Doc{Description: "Two says that."})

	if _, _, ok := Prose("GET", "/v1/contested"); ok {
		t.Fatal("answered an address two packages describe differently")
	}
	if d, ok := docFor("a/one", "GET", "/v1/contested"); !ok || d.Description != "One says this." {
		t.Fatalf("the qualified lookup broke: %+v ok=%v", d, ok)
	}
}

// An unqualified key still reads, which is what a generated file written
// before keys carried a package produces.
func TestProse_StillReadsAnUnqualifiedKey(t *testing.T) {
	Describe("GET /v1/legacy", Doc{Description: "Legacy answers the old way."})
	if _, _, ok := Prose("GET", "/v1/legacy"); !ok {
		t.Fatal("an unqualified key stopped reading")
	}
}
