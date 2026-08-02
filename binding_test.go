package zip

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The binding requirement, enforced mechanically rather than by review.
//
// Every validator and reducer consumes the walk's result; none reopens
// App.entries or recurses the tree, and downstream code switches on the
// NORMALISED occurrence kind, never on payload types. The way that stays true is
// that there is exactly one place allowed to ask what a node IS.
//
// This is the test the language document asks for by name: the only type switch
// over `node` lives in walk.go.
func TestOnlyOneTypeSwitchOverNode(t *testing.T) {
	// A switch over node necessarily has a `route` case: route is the member
	// unique to node. Component is the other sealed set (Handler | *App), and
	// switching on it is CONSTRUCTION — deciding what to append — not reduction,
	// so it is not what this rule governs.
	payload := map[string]bool{"Handler": true, "route": true, "App": true}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	offenders := map[string][]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSwitchStmt)
			if !ok {
				return true
			}
			hits, overNode := 0, false
			for _, stmt := range ts.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					name := ""
					switch e := expr.(type) {
					case *ast.Ident:
						name = e.Name
					case *ast.StarExpr:
						if id, ok := e.X.(*ast.Ident); ok {
							name = id.Name
						}
					}
					if payload[name] {
						hits++
					}
					if name == "route" {
						overNode = true
					}
				}
			}
			if hits >= 2 && overNode {
				offenders[f] = append(offenders[f], fset.Position(ts.Pos()).Line)
			}
			return true
		})
	}

	for f, lines := range offenders {
		if f != "walk.go" {
			t.Errorf("type switch over node at %s:%v — the only one may live in walk.go. "+
				"Reducers switch on the normalised occurrence kind (o.kind()), never on payload types",
				f, lines)
		}
	}
	if len(offenders["walk.go"]) == 0 {
		t.Error("no type switch over node found in walk.go — the walk is supposed to be the one place that asks what a node is")
	}
}

// The other half of the same requirement: nothing outside the walk recurses the
// tree by reopening App.entries.
func TestNothingOutsideTheWalkRecursesTheTree(t *testing.T) {
	// Construction and mutation necessarily touch entries — appending to a
	// program, and editing one between generations, is what they ARE. The rule
	// governs VALIDATORS and REDUCERS: those consume the walk's result.
	allowed := map[string]bool{
		"walk.go":       true, // the traversal, and the validation that rides it
		"compose.go":    true, // Use / Group append
		"host.go":       true, // Drop / Reload edit the program between generations
		"generation.go": true, // transact's snapshot and rollback
	}
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			rng, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}
			sel, ok := rng.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "entries" {
				return true
			}
			t.Errorf("%s:%d ranges over App.entries — every reducer consumes the walk's "+
				"result instead, so scoping semantics stay in one function",
				f, fset.Position(rng.Pos()).Line)
			return true
		})
	}
}
