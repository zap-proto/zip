package zip_test

import (
	"strings"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/internal/attribution/buckets"
)

// TestPkg_IsTheRegisteringPackage pins the question [zip.Op].Pkg answers, for
// every form a registration can take.
//
// Pkg namespaces the process-wide prose map, so it has to name the package
// zipdoc read the source from — the one where the registration is WRITTEN.
// Asking the handler instead answers the same thing only while the two sit
// together, and a helper that composes handlers elsewhere pulls them apart:
// seven metered ops once published with no description at all, because the
// closure that wrapped each handler was declared in the metering package.
//
// A scope's verb method puts one more frame between the registration and this
// package, so the three forms below have to agree.
func TestPkg_IsTheRegisteringPackage(t *testing.T) {
	app := zip.New(zip.Config{AppName: "attribution"})
	buckets.Register(app)

	m := app.Manifest()
	if len(m.Ops) != 3 {
		t.Fatalf("ops = %d, want 3", len(m.Ops))
	}
	for _, op := range m.Ops {
		if op.Pkg != buckets.Path {
			t.Errorf("%s %s: Pkg = %q, want the registering package %q",
				op.Method, op.Path, op.Pkg, buckets.Path)
		}
	}
}

// TestSite_IsTheWrittenLine pins the OTHER half of a registration's
// attribution: the file and line a conflict names.
//
// A scope's verb method sits one frame further from the programmer than the
// package-level function does, and the callsite is recorded by counting frames.
// Get that count wrong and every conflict, cycle and post-seal write in a
// scoped service points at zip's own source, which tells the reader nothing
// about the two lines actually in conflict.
func TestSite_IsTheWrittenLine(t *testing.T) {
	app := zip.New(zip.Config{AppName: "attribution"})
	buckets.Clash(app)

	err := app.Build()
	if err == nil {
		t.Fatal("Build() = nil; one address declared twice is a conflict")
	}
	msg := err.Error()
	if !strings.Contains(msg, "buckets/buckets.go:") {
		t.Errorf("conflict = %q; it has to name the line that wrote the route", msg)
	}
	if strings.Contains(msg, "zip/scope.go:") {
		t.Errorf("conflict = %q; the scope method is not the site, its caller is", msg)
	}
}
