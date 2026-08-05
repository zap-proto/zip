package zip

import "testing"

// ID is the one rule for an operation's name, so its properties are pinned here
// rather than at each surface that reads it. Every case below is a collision the
// encoding has to survive, not a spelling preference.
func TestID(t *testing.T) {
	for _, c := range []struct{ method, path, want string }{
		// The method is a word, the path is the rest, '_' encodes '/'.
		{"GET", "/v1/agents/targets", "get_v1_agents_targets"},
		{"POST", "/v1/agents/targets", "post_v1_agents_targets"},

		// A parameter is "by_<name>", which is what keeps it apart from a
		// literal segment of the same name.
		{"GET", "/v1/a/{b}", "get_v1_a_by_b"},
		{"GET", "/v1/a/b", "get_v1_a_b"},

		// Every spelling of ONE parameter reaches ONE name. The router's
		// constraint and optional marker say how a segment is MATCHED, not what
		// it is called (see Template), so they do not reach the id.
		{"GET", "/v1/invoices/:id", "get_v1_invoices_by_id"},
		{"GET", "/v1/invoices/{id}", "get_v1_invoices_by_id"},
		{"GET", "/v1/invoices/:id?", "get_v1_invoices_by_id"},
		{"GET", "/v1/invoices/:id<guid>", "get_v1_invoices_by_id"},

		// '-' and '.' are legal in an operationId and are PRESERVED, because '_'
		// is the separator and folding them collapsed these two onto one id once.
		{"GET", "/v1/pricing-policy", "get_v1_pricing-policy"},
		{"GET", "/v1/pricing/policy", "get_v1_pricing_policy"},

		// Anything else folds to '_', including a literal '_' — which is the
		// residual aliasing the encoding cannot remove and the walk's uniqueness
		// check exists to catch.
		{"GET", "/v1/a/b_c", "get_v1_a_b_c"},
		{"GET", "/v1/a/b/c", "get_v1_a_b_c"},
		{"GET", "/v1/Git/Upload-Pack", "get_v1_git_upload-pack"},

		// A wildcard declares no name, so it takes the positional one the
		// document gives it, numbered as the document numbers it.
		{"GET", "/assets/*", "get_assets_by_wildcard1"},
		{"GET", "/a/*/b/*", "get_a_by_wildcard1_b_by_wildcard2"},
		{"GET", "/assets/{wildcard1}", "get_assets_by_wildcard1"},

		// The root has no segments at all.
		{"GET", "/", "get"},
	} {
		if got := ID(c.method, c.path); got != c.want {
			t.Errorf("ID(%q, %q) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

// The router's spelling and the document's spelling of one address are the same
// operation, so they must derive the same id — this is what lets a caller read
// the published document and reach the op by the name it found there.
func TestIDAgreesAcrossRouterAndDocumentSpellings(t *testing.T) {
	for _, p := range []string{
		"/v1/invoices/:id",
		"/v1/orgs/:org/members/:user",
		"/v1/a/:b/c",
	} {
		if router, doc := ID("GET", p), ID("GET", Template(p)); router != doc {
			t.Errorf("%s: router spelling gives %q, document spelling %q — one address, two names", p, router, doc)
		}
	}
}
