// SPDX-License-Identifier: BSD-3-Clause-Eco

package zip_test

// A schema written by the Python front end, read here.
//
// py/ declares operations from `typing` at run time and writes a .zap file.
// This is the other side of that file: the same reader every projection is
// computed through, asked whether what Python wrote is what this process
// derives. Two things have to be true, and neither is a matter of taste.
//
// The OFFSETS have to agree. A .zap field states an offset and [LayoutOf]
// derives one, and [ReadZAP] refuses the file where they differ — so a front end
// that packed its fields, or aligned a nested value to four, is refused here
// rather than discovered by a peer reading eight bytes from the wrong place.
// TestPythonSchemaLaysOutAsThisProcessDoes is that check, and the mangled copy
// beside it is the check on the check.
//
// The DECLARATIONS have to be the same. A Python service and a Go service that
// declare the same operations write the same structs, the same names, the same
// order and the same interface — not similar ones — because every name here is
// one rule and not one rule per language. That is what makes the file the pivot
// rather than a fifth dialect: the projections cannot disagree about a service
// they read from identical bytes.
//
// What the two languages do NOT write identically is prose, and the difference
// is stated rather than smoothed over: see TestPythonAndGoDeclareTheSameService.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

const pySchema = "py/example/orders.zap"

func readPython(t *testing.T) *zip.App {
	t.Helper()
	src, err := os.ReadFile(pySchema)
	if err != nil {
		t.Fatalf("%v", err)
	}
	apps, err := zip.ReadZAP(pySchema, src)
	if err != nil {
		t.Fatalf("the schema Python wrote is not readable here: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("want 1 app, got %d", len(apps))
	}
	return apps[0]
}

// The offsets Python derived are the offsets this process derives. ReadZAP
// compares every field and refuses any disagreement, so reading the file at all
// is the assertion.
func TestPythonSchemaLaysOutAsThisProcessDoes(t *testing.T) {
	app := readPython(t)
	var got []string
	for _, op := range app.Registry() {
		got = append(got, op.Method+" "+op.Path)
	}
	if len(got) != 4 {
		t.Errorf("got %d ops, want 4: %v", len(got), got)
	}
	if app.Name() != "orders" {
		t.Errorf("app name = %q, want orders", app.Name())
	}
}

// And the check on the check: a schema whose offsets are NOT this layout is
// refused, naming every field that disagrees rather than the first.
func TestASchemaLaidOutDifferentlyIsRefused(t *testing.T) {
	src, err := os.ReadFile(pySchema)
	if err != nil {
		t.Fatalf("%v", err)
	}
	// Pack the fields of Receipt the way an IDL that does not align would: a
	// text slot is eight wide, so a u64 after one is at 8 here and at 4 there.
	packed := strings.Replace(string(src), "cents  u64  @8", "cents  u64  @4", 1)
	if packed == string(src) {
		t.Fatal("the schema no longer contains the field this test mangles")
	}
	_, err = zip.ReadZAP("packed.zap", []byte(packed))
	if err == nil {
		t.Fatal("a schema declaring an offset this layout does not use was accepted")
	}
	if !strings.Contains(err.Error(), "declared at @4 and lays out at @8") {
		t.Errorf("the refusal does not say what disagrees: %v", err)
	}
}

// The same service, declared in each language, is the same schema.
//
// The comparison is of the DECLARATIONS — the text a parser reads — because the
// two languages carry different prose into the file and that difference is real:
// Python has a struct's docstring and a field's `Annotated` metadata at run time
// and writes both; Go's registry holds an operation's sentence and its fields',
// and ZAPSchema writes only the operation's. The bytes that describe the wire
// are identical; the comments above them are not yet.
func TestPythonAndGoDeclareTheSameService(t *testing.T) {
	type Order struct {
		Sku   string  `json:"sku"`
		Count uint32  `json:"count"`
		Note  *string `json:"note"`
	}
	type Receipt struct {
		ID     string `json:"id"`
		Cents  uint64 `json:"cents"`
		Filled bool   `json:"filled"`
	}
	type Query struct {
		Sku   string `json:"sku"`
		Limit uint32 `json:"limit"`
	}
	type Page struct {
		Orders []Order `json:"orders"`
		Total  uint64  `json:"total"`
	}
	type Cancel struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	type Health struct {
		Ready bool   `json:"ready"`
		Since uint64 `json:"since"`
	}
	type Note struct {
		ID   string `json:"id"`
		Body any    `json:"body"`
	}

	a := zip.New(zip.Config{AppName: "orders"})
	zip.Post(a, "/v1/orders", func(context.Context, *Order) (*Receipt, error) { return nil, nil })
	zip.Get(a, "/v1/orders", func(context.Context, *Query) (*Page, error) { return nil, nil })
	// The address is spelled the router's way here and the document's way in
	// Python. Both reach one id, which is what keeps the two files one file.
	zip.Delete(a, "/v1/orders/:id", func(context.Context, *Cancel) (*struct{}, error) { return nil, nil })
	zip.Get(a, "/v1/health", func(context.Context, *struct{}) (*Health, error) { return nil, nil })
	zip.Post(a, "/v1/orders/note", func(context.Context, *Note) (*struct{}, error) { return nil, nil })

	src, err := os.ReadFile(pySchema)
	if err != nil {
		t.Fatalf("%v", err)
	}
	python := declarations(string(src))
	golang := declarations(zip.ZAPSchema("orders", a).String())
	if python != golang {
		t.Errorf("the two languages describe different services:\n--- python ---\n%s\n--- go ---\n%s",
			python, golang)
	}
	// And the prose really is in the Python file, so the comparison above is
	// ignoring something rather than nothing.
	for _, want := range []string{
		"# What a customer asks for.",
		"# The catalogue number of the thing being bought.",
		"# Place an order, which is filled from stock or refused, never queued.",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("the Python schema does not carry %q", want)
		}
	}
}

// A field that names no type is absent from both files, and named in both
// ledgers under one cause.
func TestTheOpThatCannotCrossIsAbsentFromBothLanguages(t *testing.T) {
	src, err := os.ReadFile(pySchema)
	if err != nil {
		t.Fatalf("%v", err)
	}
	text := string(src)
	if strings.Contains(text, "post_orders_note(") {
		t.Error("an op whose field names no type is in the schema")
	}
	if !strings.Contains(text, "#   post_orders_note  Note.body  Any  (any)") {
		t.Errorf("the ledger does not name it:\n%s", text)
	}
}

// declarations is the schema with its comments removed: what the parser reads,
// which is what every projection is computed from.
func declarations(zap string) string {
	var out []string
	for _, line := range strings.Split(zap, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// Every projection is computed for the Python service, from the file and
// nothing else. This is the whole point of the pivot: the front end wrote a
// schema, and the SDKs, the tool list, the CLI and the document are the ones
// this repository already had.
func TestPythonSchemaProjectsOntoEverySurface(t *testing.T) {
	app := readPython(t)
	ops := []string{"post_orders", "get_orders", "delete_orders_by_id", "get_health"}

	rust, err := app.RustSDK("orders")
	if err != nil {
		t.Fatalf("RustSDK: %v", err)
	}
	cpp, err := app.CppSDK("orders")
	if err != nil {
		t.Fatalf("CppSDK: %v", err)
	}
	golang, err := app.SDK("orders")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	docs, err := app.DocsMarkdown("Orders")
	if err != nil {
		t.Fatalf("DocsMarkdown: %v", err)
	}
	for name, text := range map[string]string{
		"the Rust SDK": string(rust.Source),
		"the C++ SDK":  string(cpp.Header),
		"the Go SDK":   string(golang.Source),
		"the docs":     docs.Index,
	} {
		for _, op := range ops {
			if !strings.Contains(text, op) {
				t.Errorf("%s does not carry %s", name, op)
			}
		}
	}

	cmds := app.Commands()
	if len(cmds) != len(ops) {
		t.Errorf("the CLI has %d commands, want %d", len(cmds), len(ops))
	}
	tools := app.MCPTools()
	if len(tools) != len(ops) {
		t.Errorf("the MCP tool list has %d tools, want %d", len(tools), len(ops))
	}

	spec := app.OpenAPISpec()
	paths, _ := spec["paths"].(map[string]map[string]any)
	if len(paths) != len(ops) {
		t.Errorf("the OpenAPI document has %d paths, want %d: %v", len(paths), len(ops), paths)
	}
	defs, _ := spec["components"].(map[string]any)["schemas"].(map[string]any)
	for _, want := range []string{"Order", "Receipt", "Query", "Page", "Cancel", "Health"} {
		if _, ok := defs[want]; !ok {
			t.Errorf("the OpenAPI document does not define %s", want)
		}
	}
	if _, ok := defs["Note"]; ok {
		t.Error("the OpenAPI document defines a struct the schema refused to declare")
	}
}
