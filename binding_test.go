package zip

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The binding requirement, enforced mechanically rather than by review.
//
// "One walk, and every projection is a reducer over its result" is the claim the
// whole design rests on, and it is the kind of claim that decays silently: a
// second reader of App.entries is four lines, looks harmless in review, reads
// exactly like the first one, and immediately becomes a second authority on what
// a composition means. That is how routesUnder came to re-walk the tree with a
// cycle guard of its own, able to disagree with the walk about the program it
// was asked about.
//
// Prose cannot hold the line and neither can review, so the line is a test. It
// parses this package's own source and refuses any read of an entry's payload,
// and any traversal of an entry list, outside the sites named below.
//
// It is deliberately SYNTACTIC. A type-checked version would be more precise and
// would need the whole dependency graph loaded to say anything at all; the four
// shapes below are the only ones the offence can take, they are cheap to
// recognise, and a test that runs in milliseconds with no toolchain
// prerequisites is a test that keeps running.
//
//	x.n.(type)      a type switch over a payload
//	x.n.(T)         a type assertion on a payload
//	case route:     a switch that names a payload type, however it was reached
//	range x.entries a traversal of a program's entry list
//
// What is NOT an offence: switching over [Component] (the Use surface is a
// different closed set, and compose is entitled to know it — deciding what to
// APPEND is construction, not reduction), and reading an occurrence's kind
// through [occurrence.kind] and the accessors, which is what every reducer does.
func TestOneInterpreter_NothingOutsideTheWalkReadsAPayloadOrWalksTheTree(t *testing.T) {
	// file/func -> why this one site is allowed. Every entry must be HIT, so
	// deleting the walk cannot make this test pass by having nothing to find.
	allowed := map[string]string{
		"walk.go/descend":    "the ONE type switch over node, and the one traversal of an entry list",
		"walk.go/kind":       "the normaliser: turns the payload into the discriminator reducers switch on",
		"walk.go/middleware": "sanctioned accessor: one assertion, no traversal",
		"walk.go/route":      "sanctioned accessor: one assertion, no traversal",
		"walk.go/app":        "sanctioned accessor: one assertion, no traversal",
		"host.go/Drop": "the WRITER of an entry list: filters the receiver's own entries " +
			"between generations, and recurses nowhere",
		"host.go/Reload": "the WRITER of an entry list: swaps a definition for a new version " +
			"of itself by name, and recurses nowhere",
	}
	hit := map[string]bool{}
	var offences []string

	for _, path := range packageFiles(t) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		base := filepath.Base(path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			site := base + "/" + fn.Name.Name
			report := func(n ast.Node, what string) {
				if _, ok := allowed[site]; ok {
					hit[site] = true
					return
				}
				offences = append(offences, fset.Position(n.Pos()).String()+"\n\t\t"+what+
					"\n\t\tin "+site+" — reduce walk's []occurrence instead")
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch s := n.(type) {
				case *ast.TypeSwitchStmt:
					if isPayload(typeSwitchSubject(s)) {
						report(s, "type switch over an entry's payload (x.n.(type))")
					} else if namesPayloadType(s) {
						report(s, "type switch naming a node payload type (case route:)")
					}
				case *ast.TypeAssertExpr:
					// A nil Type is the `x.(type)` inside the switch above,
					// already reported as the switch it belongs to.
					if s.Type != nil && isPayload(s.X) {
						report(s, "type assertion on an entry's payload (x.n.(T))")
					}
				case *ast.RangeStmt:
					if isEntries(s.X) {
						report(s, "traversal of an App's entry list (range x.entries)")
					}
				}
				return true
			})
		}
	}

	if len(offences) > 0 {
		sort.Strings(offences)
		t.Errorf("the walk is no longer the only interpreter — %d second authority/authorities:\n\t%s",
			len(offences), strings.Join(offences, "\n\t"))
	}
	for site, why := range allowed {
		if !hit[site] {
			t.Errorf("%s no longer does what it is allow-listed for (%s) — "+
				"if it moved, move the allowance; if it is gone, delete it, but do not leave this test "+
				"passing because there is nothing left to check", site, why)
		}
	}
}

// packageFiles is this package's own source, tests excluded: a test file is
// allowed to take a program apart any way it likes, because a test asserts on
// the AST rather than interpreting it for a running server.
func packageFiles(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	if len(out) < 20 {
		t.Fatalf("found %d source files; the scan is not seeing the package", len(out))
	}
	sort.Strings(out)
	return out
}

// typeSwitchSubject is the expression a type switch switches ON, for both
// spellings — `switch x := e.(type)` and `switch e.(type)`.
func typeSwitchSubject(s *ast.TypeSwitchStmt) ast.Expr {
	var e ast.Expr
	switch a := s.Assign.(type) {
	case *ast.AssignStmt:
		if len(a.Rhs) == 1 {
			e = a.Rhs[0]
		}
	case *ast.ExprStmt:
		e = a.X
	}
	if ta, ok := e.(*ast.TypeAssertExpr); ok {
		return ta.X
	}
	return nil
}

// namesPayloadType catches a switch over a payload reached under any other name,
// by looking at what it decides: `route` is a node payload and belongs to no
// other closed set in this package, so a case for it is a case over node.
func namesPayloadType(s *ast.TypeSwitchStmt) bool {
	for _, stmt := range s.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, texp := range cc.List {
			if id, ok := texp.(*ast.Ident); ok && id.Name == "route" {
				return true
			}
		}
	}
	return false
}

// isPayload reports whether e reads an entry's or an occurrence's payload field.
// Both spell it `n`, which is what makes this one predicate.
func isPayload(e ast.Expr) bool { return fieldNamed(e, "n") }

// isEntries reports whether e is an App's program.
func isEntries(e ast.Expr) bool { return fieldNamed(e, "entries") }

func fieldNamed(e ast.Expr, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == name
}
