package zip

import "encoding/json"

// Documentation comes from the source, because the source is where it is already
// written.
//
// Go does not retain doc comments at run time — reflection sees types and struct
// tags, never comments — so an operation's prose can only reach the spec through
// a build-time pass over the AST. That is the whole reason this package exists:
// without it the only runtime option is WithSummary("…") sitting directly under a
// doc comment that says the same thing, which is two places to change and one to
// forget.
//
// cmd/zipdoc walks a package, reads the doc comment on each typed handler and on
// each field of its In and Out types, and emits a file that calls Describe. The
// comment IS the description; nothing is written twice.
//
//	// ListInvoices returns every invoice for the caller's org, newest first.
//	//
//	// Example: {"limit": 25}
//	// Response: {"invoices": [{"id": "inv_1", "cents": 1200}]}
//	func ListInvoices(ctx context.Context, in *ListIn) (*ListOut, error)
//
// Examples are part of the comment rather than a struct tag for the same reason:
// one mechanism, one place. A spec without examples is a spec nobody can try, and
// "try it" is most of what an interactive doc is for.

// Doc is what cmd/zipdoc extracted for one operation.
type Doc struct {
	// Description is the handler's doc comment, minus any Example/Response lines.
	Description string

	// Fields maps a JSON field name to its doc comment, for both In and Out.
	// Keyed by "TypeName.jsonField" so an In and an Out field of the same name
	// do not collide.
	Fields map[string]string

	// Example and Response are the request and response bodies from the comment.
	// Raw JSON so they land in the spec exactly as written and are wrong loudly
	// rather than quietly if malformed.
	Example  json.RawMessage
	Response json.RawMessage
}

// docs is the process-wide extraction, keyed by the DECLARING PACKAGE and the
// operation's address.
//
// The package is part of the key because this map is one map for a whole
// process, and an address is only unique within the app that serves it. Two
// chains in one node both answer GET /height — the mount is what tells them
// apart, and it is not in the key — so whichever init() ran last silently
// published its prose for the other's route. Nothing failed: only the sentence
// was wrong, in the document and in the MCP tool, which is why it survived
// every test. Seven addresses collided that way between the P-Chain and the
// X-Chain alone.
//
// It is the same namespacing the schema registry already does for named types
// (see schemaRegistry.origin): who DECLARED a thing is a property of the code,
// and it is what keeps two declarations of one name apart.
var docs = map[string]Doc{}

// DocKey is the key a package's operation is filed under. One function, so the
// generator that writes a key and the registry that reads one cannot spell it
// differently.
func DocKey(pkg, method, path string) string { return pkg + " " + method + " " + path }

// Describe records documentation for one operation, under the key cmd/zipdoc
// built with [DocKey]. Generated code calls it from an init(); hand-written
// calls are possible but defeat the point, since the comment is then no longer
// the single source.
func Describe(key string, d Doc) { docs[key] = d }

// docFor returns the extraction for an operation, if cmd/zipdoc ran.
//
// An unqualified key is still read, because a generated file written before the
// key carried a package is still correct for a process serving one app — which
// is most of them. A qualified key wins where both exist.
func docFor(pkg, method, path string) (Doc, bool) {
	if d, ok := docs[DocKey(pkg, method, path)]; ok {
		return d, true
	}
	d, ok := docs[method+" "+path]
	return d, ok
}

// Prose is what an operation says about itself: the sentence for a one-line
// summary, and the whole doc comment for the long form. Absent when cmd/zipdoc
// found no comment for that address.
//
// It is exported because the typed-op registry is NOT the only reader of this.
// A host that composes several apps into one document projects its LIVE ROUTER —
// every route, typed or not — and the untyped half has prose here and nowhere
// else. Without this the host would have to keep its own table of strings for
// routes it does not own, which is the duplication the whole package exists to
// remove: the sentence belongs to the service that serves the route, and this is
// how it travels.
//
// The summary is derived here, not by the caller, so every projection of one
// operation shortens it identically.
func Prose(method, path string) (summary, description string, ok bool) {
	d, ok := docFor("", method, path)
	if !ok || d.Description == "" {
		return "", "", false
	}
	return firstSentence(d.Description), d.Description, true
}
