package zipdoc

import "testing"

// A host may name the router type with an alias and hand that to every plugin.
// An alias is the same type, so an op registered on one is registered on the
// app — the extractor has to see through the name to the shape. The last op
// also fixes the spelling that names a group with var rather than :=, which
// is the same call either way.
func TestExtract_ARouterNamedByAnAliasIsStillTheApp(t *testing.T) {
	p := load(t, "alias")

	for _, want := range []string{"POST /v1/ask", "POST /v1/ask/history/replay", "POST /v1/ask/audit/entries"} {
		op := opByKey(t, p, want)
		if op.Description == "" {
			t.Errorf("%s has no prose", want)
		}
	}
	if len(p.Ops) != 3 {
		var k []string
		for _, o := range p.Ops {
			k = append(k, o.Key())
		}
		t.Fatalf("ops = %d, want 3; got %v", len(p.Ops), k)
	}
}
