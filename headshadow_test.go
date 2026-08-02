package zip

import "testing"

// Does the HEAD filter distinguish a SHADOW from a declared door?
func TestHeadShadow_ExplicitHeadSurvivesTheFilter(t *testing.T) {
	a := quiet("svc")
	a.Get("/thing", func(c *Ctx) error { return nil })  // fiber shadows this with HEAD
	a.Head("/probe", func(c *Ctx) error { return nil }) // an explicitly declared door

	d := a.Declaration()
	var gotGet, gotHead bool
	for _, r := range d.Routes {
		if r.Method == "GET" && r.Pattern == "/thing" {
			gotGet = true
		}
		if r.Method == "HEAD" && r.Pattern == "/probe" {
			gotHead = true
		}
	}
	if !gotGet {
		t.Error("GET /thing missing from the declaration")
	}
	if !gotHead {
		t.Error("HEAD /probe missing — an explicitly declared HEAD is a door a host must route, " +
			"not a shadow fiber generated")
	}
	// And the shadow itself must NOT be there.
	for _, r := range d.Routes {
		if r.Method == "HEAD" && r.Pattern == "/thing" {
			t.Error("HEAD /thing is a fiber shadow and must not be declared")
		}
	}
}
