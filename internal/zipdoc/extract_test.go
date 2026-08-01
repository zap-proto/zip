package zipdoc

import (
	"path/filepath"
	"strings"
	"testing"
)

// load runs the extraction over one testdata package, the way `go generate` runs
// it: from inside the package's own directory.
func load(t *testing.T, pkg string) Package {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", pkg))
	if err != nil {
		t.Fatal(err)
	}
	pkgs, err := Load(dir, nil)
	if err != nil {
		t.Fatalf("Load(%s): %v", pkg, err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("loaded %d packages, want 1", len(pkgs))
	}
	return pkgs[0]
}

func opByKey(t *testing.T, p Package, key string) Op {
	t.Helper()
	for _, o := range p.Ops {
		if o.Key() == key {
			return o
		}
	}
	var keys []string
	for _, o := range p.Ops {
		keys = append(keys, o.Key())
	}
	t.Fatalf("no op %q; got %v", key, keys)
	return Op{}
}

// The fixture is every shape of registration zipdoc has to understand at once:
// a named handler, one built by a function closing over a dependency, an inline
// one, a group-less app, explicit type arguments and inferred ones. Its prose is
// the assertion.
func TestExtract_Fixture(t *testing.T) {
	p := load(t, "fixture")
	if len(p.Ops) != 6 {
		t.Fatalf("ops = %d, want 6", len(p.Ops))
	}

	list := opByKey(t, p, "POST /v1/billing/invoices")
	// The doc comment reads "ListInvoices returns …" in the source, as Go
	// requires; the projected prose drops the identifier, which belongs to
	// Go's namespace and not the document's.
	if !strings.HasPrefix(list.Description, "Returns every invoice") {
		t.Errorf("description = %q", list.Description)
	}
	if list.Example != `{"org":"hanzo","limit":25}` {
		t.Errorf("example = %s", list.Example)
	}
	if !strings.Contains(list.Response, `"inv_1"`) {
		t.Errorf("response = %s", list.Response)
	}
	for key, want := range map[string]string{
		"ListIn.limit":  "Limit caps how many come back. Zero means the server's default.",
		"ListIn.Cursor": "Cursor is opaque; pass back what the last page returned.",
		"ListOut.next":  "Next is the cursor for the following page, empty at the end.",
		"Invoice.cents": "Cents is the amount owed, in the org's billing currency.",
		// A line comment beside the field documents it too — there is nowhere
		// else to put one.
		"ListOut.invoices": "Invoices, newest first.",
	} {
		if got := list.Fields[key]; got != want {
			t.Errorf("Fields[%q] = %q, want %q", key, got, want)
		}
	}
	// Not on the wire, not in the spec.
	for _, absent := range []string{"ListIn.-", "ListIn.Internal", "ListIn.page"} {
		if got, ok := list.Fields[absent]; ok {
			t.Errorf("Fields[%q] = %q, want absent", absent, got)
		}
	}

	// An inline handler is documented by the comment above the registration. It
	// names no function, so the exact-name evidence cannot apply — but the comment
	// is written to the same convention and opens with the same kind of symbol, so
	// the sentence shape speaks for it and the reader gets prose either way.
	void := opByKey(t, p, "DELETE /v1/billing/invoices/:id")
	if !strings.HasPrefix(void.Description, "Voids an invoice") {
		t.Errorf("inline description = %q", void.Description)
	}
	if void.Response != `{"ok":true}` {
		t.Errorf("inline response = %s", void.Response)
	}
	if got := void.Fields["VoidOut.ok"]; got != "OK is true once the invoice is voided." {
		t.Errorf("Fields[VoidOut.ok] = %q", got)
	}

	// A handler BUILT by a function that closes over a dependency is documented
	// by that function: the closure it returns has no declaration of its own, so
	// the builder is the only place the sentence can be written. Missing this
	// shape is not a corner case — it is how a service reaches for a handler the
	// moment the handler needs a store, and every one of those ops published an
	// operationId and nothing else while the sentence sat in the source.
	pay := opByKey(t, p, "POST /v1/billing/invoices/:id/pay")
	if !strings.HasPrefix(pay.Description, "Settles an open invoice") {
		t.Errorf("built description = %q", pay.Description)
	}
	if pay.Response != `{"ok":true}` {
		t.Errorf("built response = %s", pay.Response)
	}
	// The builder's In and Out are read off the INSTANTIATION, so a built handler
	// carries field prose exactly as a named one does.
	if got := pay.Fields["PayIn.id"]; got != "ID of the invoice to settle." {
		t.Errorf("Fields[PayIn.id] = %q", got)
	}

	// The comment belongs to the REGISTRATION, not to the closure literal. They
	// are the same line only while the call fits on one — and a call carrying
	// WithSummary/WithTags never does, which is every registration a service
	// writes once it has anything to say about the op.
	refund := opByKey(t, p, "POST /v1/billing/invoices/:id/refund")
	if !strings.HasPrefix(refund.Description, "Returns a settled invoice's amount") {
		t.Errorf("multi-line inline description = %q", refund.Description)
	}
	if refund.Response != `{"ok":true}` {
		t.Errorf("multi-line inline response = %s", refund.Response)
	}

	// An UNTYPED route is an operation too. The wire keeps some routes raw — a
	// file download, an OIDC redirect, a SCIM body a standard governs — and before
	// this they had nowhere at all to say what they do, so each published an
	// address and silence. Prose only: there is no In and no Out to walk, and this
	// pass must never make a raw route look typed.
	pdf := opByKey(t, p, "GET /v1/billing/invoices/:id/pdf")
	if !strings.HasPrefix(pdf.Description, "Downloads an invoice") {
		t.Errorf("raw description = %q", pdf.Description)
	}
	if len(pdf.Fields) != 0 {
		t.Errorf("raw op carries %d field descriptions, want none", len(pdf.Fields))
	}
}

// A field's doc comment has to cross a package boundary, because the shapes DO:
// an op's In and Out live in the call plane and the op lives in the app that
// serves it. Without this, every op whose In/Out is a package away had a
// description and ZERO field descriptions — every field-level projection of the
// in-fleet call plane was nameless, in the OpenAPI document and in the MCP tool
// an agent reads to decide whether to call it.
func TestExtract_FieldDocsCrossPackageBoundaries(t *testing.T) {
	op := opByKey(t, load(t, "crosspkg"), "POST /v1/workers")
	if !strings.HasPrefix(op.Description, "Returns every worker") {
		t.Errorf("description = %q", op.Description)
	}
	for key, want := range map[string]string{
		"ListIn.org":       "Org whose workers to list.",
		"ListIn.limit":     "Limit caps how many come back.",
		"ListOut.workers":  "Workers, newest first.",
		"ListOut.next":     "Next is the cursor for the following page.",
		"Worker.name":      "Name is the worker's stable name, unique in the org.",
		"Worker.live":      "Live reports whether it is serving traffic.",
	} {
		if got := op.Fields[key]; got != want {
			t.Errorf("Fields[%q] = %q, want %q", key, got, want)
		}
	}
}

// The symbol is dropped on two evidences and no others: the handler's own name,
// exactly; failing that, Go's own sentence shape. stripSelf is a projection rule,
// not a guess about prose, so the cases that matter are the ones it must NOT
// touch — a wrong strip damages a sentence somebody wrote, a missed one leaves it
// as it was.
func TestStripSelf(t *testing.T) {
	for _, tc := range []struct{ text, name, want string }{
		// The name, exactly — the strongest evidence, and it needs no shape.
		{"RevokeKey revokes the caller's key.\n", "RevokeKey", "Revokes the caller's key.\n"},
		{"GetSQL returns one database.\n", "GetSQL", "Returns one database.\n"},
		{"Ping answers while the service is up.\n", "Ping", "Answers while the service is up.\n"},

		// The convention followed against a CONCEPTUAL name: `deleteLB` carrying
		// "DeleteLoadBalancer removes …". The majority case in a real fleet, and
		// an exact-match rule leaves every one of them in the document.
		{"DeleteLoadBalancer removes one of your org's load balancers.\n", "deleteLB",
			"Removes one of your org's load balancers.\n"},
		{"D1DatabaseList lists the databases on the account.\n", "list",
			"Lists the databases on the account.\n"},
		// An inline handler names no function; only the shape can speak for it.
		{"ListAgents returns every agent in your org.\n", "", "Returns every agent in your org.\n"},

		// A run of capitals is not a hump, so an acronym opening a sentence is an
		// ordinary word: "GPUs bill …" must not become "Bill …".
		{"GPUs bill per second while a machine runs.\n", "launch",
			"GPUs bill per second while a machine runs.\n"},
		{"HTTPHandler serves the raw request.\n", "raw", "HTTPHandler serves the raw request.\n"},
		// A different leading word — even a camel-case one — is prose, not the name.
		{"OAuth flows start here.\n", "Authorize", "OAuth flows start here.\n"},
		// The name is the SUBJECT here; dropping it leaves "Is the CI hook".
		{"CompleteDeployment is the CI hook for a queued release.\n", "complete",
			"CompleteDeployment is the CI hook for a queued release.\n"},
		// Punctuation after the second word means it is not the verb of "X does Y".
		{"ListAgents, the paged read, answers newest first.\n", "list",
			"ListAgents, the paged read, answers newest first.\n"},
		// The name with no following space (a one-word comment) is left alone.
		{"RevokeKey", "RevokeKey", "RevokeKey"},
		// A possessive is not the bare identifier.
		{"RevokeKey's twin lives elsewhere.\n", "RevokeKey", "RevokeKey's twin lives elsewhere.\n"},
		// No handler name and no symbol shape: nothing to drop.
		{"Returns every worker script, newest first.\n", "", "Returns every worker script, newest first.\n"},
	} {
		if got := stripSelf(tc.text, tc.name); got != tc.want {
			t.Errorf("stripSelf(%q, %q) = %q, want %q", tc.text, tc.name, got, tc.want)
		}
	}
}

// A malformed Example fails generation, naming the operation. A spec that ships
// an unparseable example is worse than one that ships none.
func TestExtract_MalformedExampleIsAnError(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "badexample"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, nil); err == nil {
		t.Fatal("Load accepted a malformed Example")
	} else if !strings.Contains(err.Error(), "Example") {
		t.Errorf("error = %v, want it to name the Example", err)
	}
}
