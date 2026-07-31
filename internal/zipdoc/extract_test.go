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
// a named handler, an inline one, a group-less app, explicit type arguments and
// inferred ones. Its prose is the assertion.
func TestExtract_Fixture(t *testing.T) {
	p := load(t, "fixture")
	if len(p.Ops) != 3 {
		t.Fatalf("ops = %d, want 3", len(p.Ops))
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

	// An inline handler is documented by the comment above the registration.
	// It names no function, so there is no identifier to strip — the comment
	// is carried exactly as written.
	void := opByKey(t, p, "DELETE /v1/billing/invoices/:id")
	if !strings.HasPrefix(void.Description, "VoidInvoice voids an invoice") {
		t.Errorf("inline description = %q", void.Description)
	}
	if void.Response != `{"ok":true}` {
		t.Errorf("inline response = %s", void.Response)
	}
	if got := void.Fields["VoidOut.ok"]; got != "OK is true once the invoice is voided." {
		t.Errorf("Fields[VoidOut.ok] = %q", got)
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

// The identifier is dropped only when it is exactly the handler's own name —
// stripSelf is a projection rule, not a guess about prose.
func TestStripSelf(t *testing.T) {
	for _, tc := range []struct{ text, name, want string }{
		{"RevokeKey revokes the caller's key.\n", "RevokeKey", "Revokes the caller's key.\n"},
		{"GetSQL returns one database.\n", "GetSQL", "Returns one database.\n"},
		// A different leading word — even a camel-case one — is prose, not the name.
		{"OAuth flows start here.\n", "Authorize", "OAuth flows start here.\n"},
		// The name with no following space (a one-word comment) is left alone.
		{"RevokeKey", "RevokeKey", "RevokeKey"},
		// A possessive is not the bare identifier.
		{"RevokeKey's twin lives elsewhere.\n", "RevokeKey", "RevokeKey's twin lives elsewhere.\n"},
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
