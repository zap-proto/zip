// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

// The write half, declared in Rust and projected here.
//
// The info corpus asks only questions: every op is a GET, and no address has a
// hole in it. So the rest of what a service does — writing, addressing one
// thing by name — reaches the projector for the first time here, from
// testdata/notes/manifest.json, which rust/zip/tests/ops.rs compiles and pins.
//
// One file, checked from both ends: Rust says the front end still writes it,
// this says the projections still read it. Neither needs the other's toolchain.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

func notes(t *testing.T) zip.Manifest {
	t.Helper()
	b, err := os.ReadFile("testdata/notes/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m zip.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if err := m.Check(); err != nil {
		t.Fatalf("the front end wrote a manifest this cannot read: %v", err)
	}
	m.Settle()
	return m
}

// TestNotesDocumentsTheWriteVerbs: a body rides the verbs that carry one, a
// hole in the address is a path parameter, and neither is invented by the
// language the op was declared in.
func TestNotesDocumentsTheWriteVerbs(t *testing.T) {
	doc, err := json.Marshal(zip.ProjectOpenAPI(notes(t)))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Parameters  []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
			RequestBody json.RawMessage `json:"requestBody"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatal(err)
	}

	for _, want := range []struct {
		path, method, id, param string
		body                    bool
	}{
		{"/notes", "get", "get_notes", "", false},
		{"/notes", "post", "post_notes", "", true},
		{"/notes/{name}", "get", "get_notes_by_name", "name", false},
		{"/notes/{name}", "put", "put_notes_by_name", "name", true},
		{"/notes/{name}", "patch", "patch_notes_by_name", "name", true},
		{"/notes/{name}", "delete", "delete_notes_by_name", "name", false},
	} {
		op, ok := spec.Paths[want.path][want.method]
		if !ok {
			t.Errorf("%s %s is not in the document", strings.ToUpper(want.method), want.path)
			continue
		}
		if op.OperationID != want.id {
			t.Errorf("%s %s: operationId = %q, want %q", want.method, want.path, op.OperationID, want.id)
		}
		if got := len(op.RequestBody) > 0; got != want.body {
			t.Errorf("%s %s: requestBody = %v, want %v", want.method, want.path, got, want.body)
		}
		var params []string
		for _, p := range op.Parameters {
			if p.In == "path" {
				params = append(params, p.Name)
			}
		}
		if want.param == "" {
			if len(params) != 0 {
				t.Errorf("%s %s: path parameters %v, want none", want.method, want.path, params)
			}
		} else if len(params) != 1 || params[0] != want.param {
			t.Errorf("%s %s: path parameters %v, want [%s]", want.method, want.path, params, want.param)
		}
	}
}

// TestNotesSplitsAddressFromArgument: a path parameter addresses, so it is a
// positional argument; everything else modifies, so it is a flag. The same
// split the Go front end makes, made from a manifest a Rust one wrote.
func TestNotesSplitsAddressFromArgument(t *testing.T) {
	want := map[string]struct{ args, flags string }{
		"list":   {"", ""},
		"get":    {"name", ""},
		"create": {"", "name,body"},
		"delete": {"name", ""},
		"update": {"name", "body"},
	}
	for _, c := range zip.ProjectCLI(notes(t)) {
		w, ok := want[c.Name]
		if !ok {
			t.Errorf("unexpected command %s %s", c.Service, c.Name)
			continue
		}
		var args, flags []string
		for _, a := range c.Args {
			args = append(args, a.Name)
		}
		for _, f := range c.Flags {
			flags = append(flags, f.Name)
		}
		if got := strings.Join(args, ","); got != w.args {
			t.Errorf("%s: args = %q, want %q", c.Name, got, w.args)
		}
		if got := strings.Join(flags, ","); got != w.flags {
			t.Errorf("%s: flags = %q, want %q", c.Name, got, w.flags)
		}
	}
}

// TestNotesSchemaNamesEveryMethod: the ZAP projection is the one that can
// refuse, so what it DOES carry is worth stating. Nothing here has a map or a
// self-describing scalar, so nothing is refused and every op crosses.
func TestNotesSchemaNamesEveryMethod(t *testing.T) {
	got := zip.ProjectZAP("notes", notes(t)).String()
	for _, want := range []string{
		"struct Named {\n    name text @0\n}",
		"struct Note {\n    name text @0\n    body text @8\n}",
		"interface notes {",
		"post_notes(req: Note) returns (rep: Note)",
		"delete_notes_by_name(req: Named) returns (rep: Named)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the schema does not carry:\n%s\n--- got\n%s", want, got)
		}
	}
	if strings.Contains(got, "blocked") || strings.Contains(got, "opaque") {
		t.Errorf("nothing here should be refused, and something was:\n%s", got)
	}
}
