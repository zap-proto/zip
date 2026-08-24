package zip

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

// THE INVERSE IS TESTED AGAINST THE THING IT INVERTS, not against a table of
// strings someone wrote down. bindURL is the rule; Address has to be that rule
// read backwards, so every case here binds a value in and reads it out again.

type addrIn struct {
	TraceID string `json:"-" url:"traceId"`
	ID      string `json:"id"`
	Limit   int    `json:"limit"`
	On      bool   `json:"on"`
	Skip    string `json:"skip" url:"-"`
}

func TestAddressIsBindURLBackwards(t *testing.T) {
	// What the router matched.
	matched := map[string]string{"traceId": "abc-123", "id": "7", "limit": "25", "on": "true"}
	var in addrIn
	bindURL(&in, matched)

	for _, c := range []struct{ pattern, want string }{
		{"/v1/traces/:traceId", "/v1/traces/abc-123"},
		{"/v1/traces/:traceId/spans/:id", "/v1/traces/abc-123/spans/7"},
		{"/v1/pages/:limit", "/v1/pages/25"},
		{"/v1/flags/:on", "/v1/flags/true"},
		{"/v1/static/path", "/v1/static/path"},
		// A constrained or optional segment names the same parameter.
		{"/v1/traces/:traceId<guid>", "/v1/traces/abc-123"},
		{"/v1/traces/:traceId?", "/v1/traces/abc-123"},
		// Case folds, exactly as bindURL's match does.
		{"/v1/traces/:TRACEID", "/v1/traces/abc-123"},
	} {
		if got := Address(c.pattern, &in); got != c.want {
			t.Errorf("Address(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

// A field the input carries only in the BODY is not a URL value, in both
// directions: bindURL will not write it and Address will not read it.
func TestAddressHonoursURLOptOut(t *testing.T) {
	in := addrIn{Skip: "written"}
	if got := Address("/v1/x/:skip", &in); got != "/v1/x/" {
		t.Errorf("Address = %q, want the parameter empty — `url:\"-\"` opts a field out of the URL", got)
	}
}

// An input that names no such parameter renders it empty rather than panicking —
// the same silence bindURL keeps for a matched segment no field claims.
func TestAddressUnnamedParameterIsEmpty(t *testing.T) {
	in := addrIn{}
	if got := Address("/v1/x/:nobody", &in); got != "/v1/x/" {
		t.Errorf("Address = %q, want /v1/x/", got)
	}
	if got := Address("/v1/x/:nobody", nil); got != "/v1/x/" {
		t.Errorf("Address(nil) = %q, want /v1/x/", got)
	}
}

// Promotion counts: encoding/json promotes an embedded struct's fields, bindURL
// binds through the promotion (wireFields), so Address reads through it too.
func TestAddressReadsPromotedFields(t *testing.T) {
	type inner struct {
		Ruleset string `json:"ruleset"`
	}
	type outer struct {
		inner
		Name string `json:"name"`
	}
	var in outer
	bindURL(&in, map[string]string{"ruleset": "default", "name": "x"})
	if got := Address("/v1/:ruleset/:name", &in); got != "/v1/default/x" {
		t.Errorf("Address = %q, want /v1/default/x", got)
	}
}

func TestTemplateIsThePublicSpelling(t *testing.T) {
	for _, c := range []struct{ pattern, want string }{
		{"/v1/traces/:traceId", "/v1/traces/{traceId}"},
		{"/v1/sentry/:project<guid>/envelope/", "/v1/sentry/{project}/envelope/"},
		{"/v1/x/:id?", "/v1/x/{id}"},
		{"/v1/static", "/v1/static"},
		{"/v1/a/:b/c/:d", "/v1/a/{b}/c/{d}"},

		// A wildcard is a segment too. It is numbered in path order, which is
		// what lets it share a name with a sibling on another path.
		{"/v1/dns/*", "/v1/dns/{wildcard1}"},
		{"/v1/kms/secrets/+", "/v1/kms/secrets/{wildcard1}"},
		{"/v1/a/*/b/*", "/v1/a/{wildcard1}/b/{wildcard2}"},
		{"/v1/:org/files/*", "/v1/{org}/files/{wildcard1}"},

		// Only a segment that IS the marker; a literal that merely contains one
		// is a name, not a wildcard.
		{"/v1/a+b/c*d", "/v1/a+b/c*d"},
	} {
		if got := Template(c.pattern); got != c.want {
			t.Errorf("Template(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

// TestTemplateAndIDSpellAWildcardTheSameWay is the property that makes the
// wildcard case above safe rather than merely chosen: [ID] reads the ROUTER's
// spelling and Template writes the DOCUMENT's, so a consumer holding either must
// arrive at one operation. Both name the segment wildcardN and both number it in
// path order, so the two cannot drift into naming one address two things.
func TestTemplateAndIDSpellAWildcardTheSameWay(t *testing.T) {
	for _, pattern := range []string{
		"/v1/dns/*",
		"/v1/kms/secrets/+",
		"/v1/a/*/b/*",
		"/v1/:org/files/*",
	} {
		router, doc := ID("GET", pattern), ID("GET", Template(pattern))
		if router != doc {
			t.Errorf("ID disagrees about %q: router spelling %q, document spelling %q",
				pattern, router, doc)
		}
	}
}

// TestTheSpecSpellsAWildcardTheDocumentsWay asks the DOCUMENT, not Template.
// Template being right buys nothing on its own: buildOpenAPI used to translate
// the path itself and only fall back to Template when its own attempt left an
// unclosed brace, so a wildcard — which has no "/:" to replace — never reached
// Template at all and "*" was published verbatim. Fixing the rule and fixing the
// caller are two changes, and only this one can tell.
func TestTheSpecSpellsAWildcardTheDocumentsWay(t *testing.T) {
	app := New(Config{AppName: "spelling", DisableStartupMessage: true})
	g := app.Group("/v1/probe")
	Get(g, "/*", func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	Get(g, "/traces/:traceId", func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	// Read it the way a consumer does — through JSON — rather than by asserting a
	// Go type onto it. A wrong assertion yields nil, and nil has no keys, so the
	// test would report the document empty whatever the document says.
	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("the document carries no paths at all — this test would pass vacuously")
	}
	for _, want := range []string{"/v1/probe/{wildcard1}", "/v1/probe/traces/{traceId}"} {
		if _, ok := doc.Paths[want]; !ok {
			t.Errorf("the document does not carry %q; it carries %v", want, slices.Sorted(maps.Keys(doc.Paths)))
		}
	}
}

// TestAWildcardIsDECLAREDAndNotJustSpelled is the half naming the segment does
// not buy, and the half that decides whether the operation is usable.
//
// OpenAPI requires every variable in a path template to be declared as a path
// parameter. Writing {wildcard1} into the path without declaring it leaves the
// one value the address IS undeclared — and worse than undeclared, because the
// field binding it is then described as a QUERY parameter named *1. A client
// generated from that has no argument for the address, sends the braces
// literally, and gets a 404 from the very route it was generated for.
//
// So this asks for the declaration, and asks that the router's own capture key
// does NOT appear as a query parameter. Both halves matter: the first was
// missing, and the second is how its absence showed up.
func TestAWildcardIsDECLAREDAndNotJustSpelled(t *testing.T) {
	app := New(Config{AppName: "declared", DisableStartupMessage: true})
	type in struct {
		Ref string `json:"-" url:"*1"`
	}
	Get(app.Group("/v1/probe"), "/*", func(context.Context, *in) (*struct{}, error) { return nil, nil })

	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
				Req  bool   `json:"required"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}

	op, served := doc.Paths["/v1/probe/{wildcard1}"]["get"]
	if !served {
		t.Fatalf("the wildcard op is not published at its template name; the document carries %v",
			slices.Sorted(maps.Keys(doc.Paths)))
	}
	var declared bool
	for _, p := range op.Parameters {
		if p.In == "query" {
			t.Errorf("the capture is published as a QUERY parameter %q — it is the address, not a "+
				"filter, so a generated client would have no argument for the path", p.Name)
		}
		if p.Name == "wildcard1" && p.In == "path" && p.Req {
			declared = true
		}
	}
	if !declared {
		t.Errorf("the path carries {wildcard1} and declares no path parameter for it; it declares %v",
			op.Parameters)
	}
}
