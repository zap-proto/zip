// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// One service, written twice.
//
// corpus/info declares fourteen operations in Go; rust/examples/info declares
// the same fourteen in Rust, natively — a proc-macro reads the signature, the
// route and the doc comment, and writes the manifest as the crate compiles.
// Both manifests then go through the SAME projector, and what comes out must be
// the same bytes.
//
// That is the whole claim, and it is mechanical: if these pass, zip is one
// framework with two implementations of its front end. If they fail, the two
// have drifted, and the diff says exactly where.
//
// The Rust side's documents are checked in (rust/examples/info/gen), the way a
// generated file is checked in anywhere else, so this test needs no cargo and
// no zipc — and regenerating them is what makes a change to the Rust source
// show up here.

func rustSide(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("rust/examples/info/gen/" + name)
	if err != nil {
		t.Skipf("the Rust corpus has not been projected: %v", err)
	}
	return b
}

func goSide(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestGoAndRustProjectOneOpenAPI(t *testing.T) {
	want := goSide(t, corpus().OpenAPISpec())
	if got := rustSide(t, "openapi.json"); string(got) != string(want) {
		t.Errorf("the two front ends publish different documents\n--- go\n%s\n--- rust\n%s", want, got)
	}
}

func TestGoAndRustProjectOneToolList(t *testing.T) {
	want := goSide(t, corpus().MCPTools())
	if got := rustSide(t, "mcp.json"); string(got) != string(want) {
		t.Errorf("the two front ends publish different tools\n--- go\n%s\n--- rust\n%s", want, got)
	}
}

func TestGoAndRustProjectOneCommandTree(t *testing.T) {
	want := goSide(t, corpus().Commands())
	if got := rustSide(t, "cli.json"); string(got) != string(want) {
		t.Errorf("the two front ends publish different commands\n--- go\n%s\n--- rust\n%s", want, got)
	}
}

// The ZAP schemas agree about the CONTRACT — the structs, their field types,
// their offsets and the interface — and differ in two places that are facts
// about the language and not about the operation: a field's identifier follows
// the source's own case convention, and the ledger names a type the way the
// person who must go and change it would grep for it.
//
// This measures that rather than asserting it. Folding the identifier case
// leaves only the ledger's comment lines, and if anything else ever differs the
// count goes up and the test says which line.
func TestGoAndRustProjectOneZAPContract(t *testing.T) {
	want := zip.ZAPSchema("info", corpus()).String()
	got := string(rustSide(t, "info.zap"))
	a, b := fold(want), fold(got)
	if len(a) != len(b) {
		t.Fatalf("the two schemas are %d and %d lines", len(a), len(b))
	}
	for i := range a {
		if a[i] == b[i] {
			continue
		}
		if isLedger(a[i]) && isLedger(b[i]) {
			continue // a source-language spelling, addressed to a reader
		}
		t.Errorf("line %d of the schema differs\n  go  : %s\n  rust: %s", i+1, a[i], b[i])
	}
}

// fold reads a schema into lines with every field identifier's case and
// underscores removed, which is the one difference a language's own naming
// convention makes to a contract both compile to the same accessors from.
func fold(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		m := fieldLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		flat := strings.ToLower(strings.ReplaceAll(m[2], "_", ""))
		lines[i] = m[1] + flat + " " + m[3] + m[4]
	}
	return lines
}

// fieldLine is one declared slot: an indent, a name, a type and an offset.
var fieldLine = regexp.MustCompile(`^(\s+)(\w+)\s+(\S+)(\s+@\d+)$`)

// isLedger reports a comment line, which is where the schema addresses a person
// rather than a compiler.
func isLedger(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "#") }
