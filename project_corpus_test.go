// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"encoding/json"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/corpus/info"
)

// The manifest lost nothing.
//
// Every projection here is produced twice — once off the Go registry, once off
// the neutral description of it — and compared byte for byte. Equality is what
// says a Rust or C++ front end that fills a manifest in has everything Go has;
// inequality names exactly the field a description cannot yet carry.

func corpus() *zip.App { return (&info.Info{Release: "luxd/1.36.178", Network: 96369}).Ops() }

func same(t *testing.T, what string, a, b any) {
	t.Helper()
	x, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	y, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if string(x) != string(y) {
		t.Errorf("%s: registry and manifest disagree\n--- registry\n%s\n--- manifest\n%s", what, x, y)
	}
}

// TestProjectOpenAPIMatchesRegistry compares the shipped document against the
// one derived straight from the Go types.
//
// It has to reach for buildOpenAPIReflect by name. App.OpenAPISpec IS
// ProjectOpenAPI(a.Manifest()) now — Go publishes the manifest-derived document
// and keeps no second path to the same file — so a test asking the App and the
// projector would be asking one function twice and would pass whatever either
// did. The reflect derivation is kept for exactly this: it is the only
// independent statement of what the document should say.
func TestProjectOpenAPIMatchesRegistry(t *testing.T) {
	app := corpus()
	same(t, "openapi", zip.OpenAPIByReflection(app), zip.ProjectOpenAPI(app.Manifest()))
}

// The tool list and the command tree have no such twin: their reflect
// derivations were replaced rather than kept, so there is nothing here to
// compare them against and a test that asked the App for them would be the
// tautology above. What holds them is this package's own suite, which was
// written against the reflect derivation and still passes, and
// conformance_test.go, where the same fourteen ops declared in Rust project to
// the same bytes. Both of those are real; a comparison of ProjectMCP with
// itself would not be.

func TestCorpusIsFourteenOps(t *testing.T) {
	if n := len(corpus().Registry()); n != 14 {
		t.Fatalf("corpus has %d ops, want 14", n)
	}
}

func TestProjectZAPMatchesRegistry(t *testing.T) {
	app := corpus()
	want := zip.ZAPSchema("info", app).String()
	got := zip.ProjectZAP("info", app.Manifest()).String()
	if want != got {
		t.Errorf("zap: registry and manifest disagree\n--- registry\n%s\n--- manifest\n%s", want, got)
	}
}
