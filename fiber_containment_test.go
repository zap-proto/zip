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

// fiberEscapeHatches are the ONLY exported functions allowed to name a fiber
// type. Both are documented escape hatches, and both hang off a CONCRETE type —
// [App] and [Ctx] — never off [Router].
//
// That distinction is the whole containment rule. Router is the type a decorator
// implements; requiring fiber there made the interface unimplementable outside
// this package, which is what stalled the v1.19 adoption in hanzoai/cloud and
// hanzoai/commerce. Wanting the router now forces you to hold the concrete value,
// which is a visible choice rather than a quiet widening of every Router in the
// estate.
var fiberEscapeHatches = map[string]bool{
	"App.Fiber": true,
	"Ctx.Fiber": true,
}

// TestFiberDoesNotLeakIntoTheExportedSurface reads this package's own AST and
// fails when an exported function signature names a fiber type outside the two
// sanctioned hatches.
//
// It is a test rather than a review habit because the leaks arrive one
// convenience at a time, and each looks reasonable alone. App.Test carried a
// fiber.TestConfig while its doc comment told callers to use it INSTEAD of
// reaching through App.Fiber — so a test that merely wanted a timeout imported
// the router this package exists to keep out of its callers, and the advice
// pointed back at the thing it was replacing.
func TestFiberDoesNotLeakIntoTheExportedSurface(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	var leaks []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			qualified := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				recv := recvTypeName(fn.Recv.List[0].Type)
				if !ast.IsExported(recv) {
					continue // a method on an unexported type is not public surface
				}
				qualified = recv + "." + fn.Name.Name
			}
			if fiberEscapeHatches[qualified] {
				continue
			}
			if sig := renderSignature(fset, fn); strings.Contains(sig, "fiber.") {
				leaks = append(leaks, name+": "+qualified+sig)
			}
		}
	}

	if len(leaks) > 0 {
		t.Errorf("fiber leaked into %d exported signature(s):\n  %s\n\n"+
			"Give zip its own type for what the caller actually needs (see TestConfig), or, if\n"+
			"this genuinely is an escape hatch, hang it off a concrete type and add it to\n"+
			"fiberEscapeHatches with the reason.", len(leaks), strings.Join(leaks, "\n  "))
	}
}

// recvTypeName is the identifier of a receiver type, pointer or not.
func recvTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver
		return recvTypeName(t.X)
	}
	return ""
}

// renderSignature flattens params and results to text — enough to spot a
// qualified fiber type without depending on a printer's formatting.
func renderSignature(fset *token.FileSet, fn *ast.FuncDecl) string {
	var b strings.Builder
	b.WriteString("(")
	if fn.Type.Params != nil {
		for i, p := range fn.Type.Params.List {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(exprText(fset, p.Type))
		}
	}
	b.WriteString(")")
	if fn.Type.Results != nil {
		b.WriteString(" ")
		for i, r := range fn.Type.Results.List {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(exprText(fset, r.Type))
		}
	}
	return b.String()
}

func exprText(fset *token.FileSet, e ast.Expr) string {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		return exprText(fset, t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + exprText(fset, t.X)
	case *ast.Ellipsis:
		return "..." + exprText(fset, t.Elt)
	case *ast.ArrayType:
		return "[]" + exprText(fset, t.Elt)
	case *ast.MapType:
		return "map[" + exprText(fset, t.Key) + "]" + exprText(fset, t.Value)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// dispatch is the path a call takes from a protocol to a handler: the MCP door
// and everything it reaches. None of it may name a fiber type — not in a
// signature, not in a body.
//
// This is the rule the inversion exists to make true, so it is the rule worth
// pinning. Before, mcpCall took a fiber.Ctx and read the caller's headers off
// it, which meant a tools/call could not happen unless an HTTP request already
// had: the transport sat UNDERNEATH the protocol. The regression that would
// undo it is not dramatic — it is one convenience, "just read fc.Get here" —
// and it would pass every behavioural test in this package, because the HTTP
// door would keep working. Only the ZAP door would quietly die, and only for
// whatever this one line needed.
var dispatch = map[string]bool{
	"App.MCP":    true, // the door itself
	"App.tool":   true, // tools/call
	"App.answer": true, // one tool result
	"App.list":   true, // tools/list
	"App.source": true, // the per-caller half
	"App.ask":    true, // an open plugin's half
	"App.relay":  true, // the hop to a plugin that owns the tool
}

func TestFiberIsNotInTheDispatchPath(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "mcp.go", nil, 0)
	if err != nil {
		t.Fatalf("parse mcp.go: %v", err)
	}

	seen := map[string]bool{}
	var leaks []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		name := recvTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
		if !dispatch[name] {
			continue
		}
		seen[name] = true
		// The WHOLE function, signature and body: a fiber type reached for
		// inside a body is the same braiding as one in a parameter, and it is
		// the one that arrives by accident.
		ast.Inspect(fn, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "fiber" {
				leaks = append(leaks, name+" names fiber."+sel.Sel.Name+
					" at "+fset.Position(sel.Pos()).String())
			}
			return true
		})
	}

	for name := range dispatch {
		if !seen[name] {
			t.Errorf("%s is in the dispatch set but no longer exists in mcp.go — "+
				"rename it here, or drop it if the path really did change shape", name)
		}
	}
	if len(leaks) > 0 {
		t.Errorf("fiber reached into the dispatch path %d time(s):\n  %s\n\n"+
			"The door takes a *zapmcp.Frame so it can be served over ZAP with no HTTP\n"+
			"listener at all (see TestMCP_ServesOverZapWithNoHTTPListener). Whatever this\n"+
			"needed from the request belongs on the ctx, put there by the ADAPTER that\n"+
			"has one — see headerOf and callerContext.", len(leaks), strings.Join(leaks, "\n  "))
	}
}
