// Package zipdoc extracts the documentation a service has already written — the
// doc comments on its typed handlers and on the fields of their In/Out types —
// and emits the zip.Describe calls that carry it into the OpenAPI spec and the
// MCP tool list at run time.
//
// Go drops comments at compile time; reflection sees types and tags and never
// prose. So the ONLY way for a doc comment to reach the spec is a build-time
// pass over the AST, and this is it. cmd/zipdoc is the command around this
// package; the split is CLI (flags, files, exit codes) from extraction
// (source in, Doc out).
package zipdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/zap-proto/zip/internal/jsontag"
)

// ZipPkg is the package whose generic registrars this pass recognises. Matching
// on the import path rather than on the local name means an aliased import, a
// dot import and a plain one all resolve the same.
const ZipPkg = "github.com/zap-proto/zip"

// verbs are the typed registrars and the method each one registers. An untyped
// app.Get has no In/Out and therefore no schema, no MCP tool and nothing here to
// document — the typed surface IS the documented surface.
var verbs = map[string]string{
	"Get":    "GET",
	"Post":   "POST",
	"Put":    "PUT",
	"Patch":  "PATCH",
	"Delete": "DELETE",
}

// Op is one typed registration and everything its source says about it. The
// fields mirror zip.Doc — emit.go writes them straight into a zip.Doc literal,
// and a test pins the two shapes together.
type Op struct {
	Method string
	Path   string

	Description string
	Fields      map[string]string
	// Example and Response are compacted JSON, "" when the comment gave none.
	Example  string
	Response string
}

// Key is the operation's identity, the same one zip's registry uses.
func (o Op) Key() string { return o.Method + " " + o.Path }

// Package is one loaded package and the operations registered in it.
type Package struct {
	Dir  string // directory the generated file belongs in
	Name string // package clause
	Path string // import path, so a package inside zip itself skips the import
	Ops  []Op
}

// Load type-checks the packages matched by patterns (relative to dir) and
// extracts every typed registration in them.
//
// It insists the packages compile. Extraction reads resolved types — which
// generic instantiation a call is, which declaration a handler name refers to —
// so a package that does not type-check yields answers that are wrong rather
// than missing, and wrong is what this whole design exists to prevent.
func Load(dir string, patterns []string) ([]Package, error) {
	if len(patterns) == 0 {
		patterns = []string{"."}
	}
	fset := token.NewFileSet()
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:  dir,
		Fset: fset,
	}, patterns...)
	if err != nil {
		return nil, err
	}
	var errs []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			errs = append(errs, p.PkgPath+": "+e.Error())
		}
	}
	if len(errs) > 0 {
		if len(errs) > 5 {
			errs = append(errs[:5], fmt.Sprintf("(+%d more)", len(errs)-5))
		}
		return nil, fmt.Errorf("packages do not compile:\n\t%s", strings.Join(errs, "\n\t"))
	}

	e := &extractor{load: fset, own: token.NewFileSet(), files: map[string]*ast.File{}}
	out := make([]Package, 0, len(pkgs))
	for _, p := range pkgs {
		ops, err := e.pkg(p)
		if err != nil {
			return nil, err
		}
		files := p.GoFiles
		if len(files) == 0 {
			files = p.CompiledGoFiles
		}
		if len(files) == 0 {
			continue
		}
		out = append(out, Package{
			Dir:  filepath.Dir(files[0]),
			Name: p.Name,
			Path: p.PkgPath,
			Ops:  ops,
		})
	}
	return out, nil
}

// extractor holds the two views of the source it needs: the loader's positions,
// and its own re-parse of each file WITH comments, which is where the prose is.
// One parse per file, cached; both views index the same bytes, so a position in
// one is a byte offset in the other.
type extractor struct {
	load  *token.FileSet
	own   *token.FileSet
	files map[string]*ast.File
}

func (e *extractor) pkg(p *packages.Package) ([]Op, error) {
	var ops []Op
	var err error
	for _, f := range p.Syntax {
		prefixes := groupPrefixes(p.TypesInfo, f)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || err != nil {
				return err == nil
			}
			op, found, cerr := e.call(p.TypesInfo, call, prefixes)
			if cerr != nil {
				err = cerr
				return false
			}
			if found {
				ops = append(ops, op)
			}
			return true
		})
	}
	return ops, err
}

// call reads one zip.Get[In,Out](app, path, handler) — or Post/Put/Patch/Delete
// — into an Op. Anything that is not such a call is reported as not found.
func (e *extractor) call(info *types.Info, call *ast.CallExpr, prefixes map[types.Object]string) (Op, bool, error) {
	id := calleeIdent(call.Fun)
	if id == nil {
		return Op{}, false, nil
	}
	fn, _ := info.Uses[id].(*types.Func)
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != ZipPkg {
		return Op{}, false, nil
	}
	method, ok := verbs[fn.Name()]
	if !ok || len(call.Args) < 3 {
		return Op{}, false, nil
	}
	// The instantiation, not the syntax: zip.Post(app, p, validate) infers In and
	// Out from the handler and writes no type arguments at all, and a registration
	// this pass cannot see is a hole in the spec.
	args := info.Instances[id].TypeArgs
	if args == nil || args.Len() != 2 {
		return Op{}, false, nil
	}

	lit := info.Types[call.Args[1]].Value
	if lit == nil || lit.Kind() != constant.String {
		return Op{}, false, fmt.Errorf("%s: zip.%s route path is not a constant string, so the operation has no identity to document",
			e.load.Position(call.Args[1].Pos()), fn.Name())
	}
	path := constant.StringVal(lit)

	// The op's identity is the WHOLE path — a group's prefix composed with the
	// leaf, exactly as zip composes it at registration. Filing prose under the
	// leaf alone is how a doc comment on a group-declared op vanished from both
	// the document and the MCP tool list: docFor looks up the composed path and
	// never matched.
	prefix, perr := routerPrefix(info, prefixes, call.Args[0])
	if perr != nil {
		return Op{}, false, fmt.Errorf("%s: zip.%s: %w", e.load.Position(call.Args[0].Pos()), fn.Name(), perr)
	}
	path = joinPath(prefix, path)

	doc := e.handlerDoc(info, call.Args[2])
	prose, example, response, err := splitDoc(doc)
	if err != nil {
		return Op{}, false, fmt.Errorf("%s: %s %s: %w", e.load.Position(call.Pos()), method, path, err)
	}
	op := Op{
		Method:      method,
		Path:        path,
		Description: prose,
		Example:     example,
		Response:    response,
		Fields:      map[string]string{},
	}
	seen := map[*types.Named]bool{}
	e.fields(args.At(0), op.Fields, seen)
	e.fields(args.At(1), op.Fields, seen)
	return op, true, nil
}

// groupPrefixes maps each variable that holds a router to the path prefix it was
// created with, so a typed op declared on a group can be filed under the path it
// actually serves. One forward pass is enough: Go requires a variable to be
// declared before it is used, so a group built from another group is always
// assigned after the one it derives from.
func groupPrefixes(info *types.Info, f *ast.File) map[types.Object]string {
	out := map[types.Object]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		obj := info.Defs[id]
		if obj == nil {
			obj = info.Uses[id]
		}
		if obj == nil {
			return true
		}
		if p, ok := groupCallPrefix(info, out, as.Rhs[0]); ok {
			out[obj] = p
		}
		return true
	})
	return out
}

// groupCallPrefix reads `<router>.Group("<literal>")` into the full prefix it
// yields, composing with the receiver's own prefix when the receiver is a group.
func groupCallPrefix(info *types.Info, known map[types.Object]string, e ast.Expr) (string, bool) {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) < 1 {
		return "", false
	}
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Group" {
		return "", false
	}
	lit := info.Types[call.Args[0]].Value
	if lit == nil || lit.Kind() != constant.String {
		return "", false
	}
	outer := ""
	if id, ok := ast.Unparen(sel.X).(*ast.Ident); ok {
		if obj := info.Uses[id]; obj != nil {
			outer = known[obj]
		}
	} else if p, ok := groupCallPrefix(info, known, sel.X); ok {
		outer = p
	}
	return joinPath(outer, constant.StringVal(lit)), true
}

// routerPrefix is the prefix an op registered on this router sits under. An *App
// is the root and has none; a group must be one this pass RESOLVED, because a
// prefix it cannot see is prose filed under the wrong identity — which is
// exactly the silent drop this resolution exists to end. So an unresolvable
// router is an error naming the call, never an assumed empty prefix.
func routerPrefix(info *types.Info, prefixes map[types.Object]string, arg ast.Expr) (string, error) {
	if isZipApp(info.Types[arg].Type) {
		return "", nil
	}
	if id, ok := ast.Unparen(arg).(*ast.Ident); ok {
		if obj := info.Uses[id]; obj != nil {
			if p, ok := prefixes[obj]; ok {
				return p, nil
			}
		}
	}
	if p, ok := groupCallPrefix(info, prefixes, arg); ok {
		return p, nil
	}
	return "", fmt.Errorf("cannot resolve the path prefix of the router this op registers on, so its doc comment " +
		"would be filed under the wrong path and silently dropped from the document and the MCP tool. " +
		"Register on the *zip.App, or on a group assigned in this file as `g := <router>.Group(\"/prefix\")`")
}

// isZipApp reports whether t is *zip.App — the root router, which has no prefix.
func isZipApp(t types.Type) bool {
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == ZipPkg && named.Obj().Name() == "App"
}

// joinPath composes a prefix with a leaf the way the router does, so the key this
// pass writes is the path zip registered.
func joinPath(prefix, path string) string {
	if path == "" {
		path = "/"
	}
	if prefix == "" {
		return path
	}
	if path[0] != '/' {
		path = "/" + path
	}
	return strings.TrimRight(prefix, "/") + path
}

// calleeIdent is the identifier being called, past any parens and any explicit
// type arguments: zip.Get, zip.Get[A,B], Get and Get[A,B] all yield the Get.
func calleeIdent(fun ast.Expr) *ast.Ident {
	fun = ast.Unparen(fun)
	switch e := fun.(type) {
	case *ast.IndexExpr:
		fun = ast.Unparen(e.X)
	case *ast.IndexListExpr:
		fun = ast.Unparen(e.X)
	}
	switch e := fun.(type) {
	case *ast.Ident:
		return e
	case *ast.SelectorExpr:
		return e.Sel
	}
	return nil
}

// handlerDoc is the comment that documents the operation: the doc comment of the
// named handler, or — when the handler is written inline — the comment directly
// above the registration, which is the only place there is to write one.
func (e *extractor) handlerDoc(info *types.Info, arg ast.Expr) string {
	switch h := ast.Unparen(arg).(type) {
	case *ast.Ident, *ast.SelectorExpr:
		var id *ast.Ident
		if s, ok := h.(*ast.SelectorExpr); ok {
			id = s.Sel
		} else {
			id = h.(*ast.Ident)
		}
		if fn, ok := info.Uses[id].(*types.Func); ok {
			if d := e.funcDecl(fn.Pos()); d != nil {
				return d.Doc.Text()
			}
		}
	case *ast.FuncLit:
		return e.commentAbove(arg.Pos())
	}
	return ""
}

// fields records the doc comment of every exported field of t, keyed exactly as
// zip's schema builder looks it up: the type's name, a dot, and the field's JSON
// name. It descends through pointers, slices, arrays and map values because the
// schema builder does, so a nested type's prose lands too.
func (e *extractor) fields(t types.Type, out map[string]string, seen map[*types.Named]bool) {
	switch t := t.(type) {
	case *types.Pointer:
		e.fields(t.Elem(), out, seen)
	case *types.Slice:
		e.fields(t.Elem(), out, seen)
	case *types.Array:
		e.fields(t.Elem(), out, seen)
	case *types.Map:
		e.fields(t.Elem(), out, seen)
	case *types.Named:
		if seen[t] {
			return
		}
		seen[t] = true
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			e.fields(t.Underlying(), out, seen)
			return
		}
		e.structFields(reflectName(t), t.Obj(), st, out)
		for i := 0; i < st.NumFields(); i++ {
			e.fields(st.Field(i).Type(), out, seen)
		}
	case *types.Struct:
		// An anonymous struct has no name to key on — the schema builder inlines
		// it — but its named field types still deserve their prose.
		for i := 0; i < t.NumFields(); i++ {
			e.fields(t.Field(i).Type(), out, seen)
		}
	}
}

// structFields pairs the type-checked fields (authoritative for names and tags)
// with the parsed ones (which carry the comments). go/types preserves source
// order, so index i is the same field in both.
func (e *extractor) structFields(name string, obj *types.TypeName, st *types.Struct, out map[string]string) {
	spec := e.typeSpec(obj.Pos(), obj.Name())
	if spec == nil {
		return
	}
	lit, ok := spec.Type.(*ast.StructType)
	if !ok {
		return
	}
	var flat []*ast.Field
	for _, f := range lit.Fields.List {
		n := len(f.Names)
		if n == 0 {
			n = 1 // embedded: one field, named for its type
		}
		for i := 0; i < n; i++ {
			flat = append(flat, f)
		}
	}
	if len(flat) != st.NumFields() {
		return
	}
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		jsonName := jsontag.Name(f.Name(), reflect.StructTag(st.Tag(i)).Get("json"))
		if jsonName == "-" {
			continue
		}
		doc := strings.TrimSpace(flat[i].Doc.Text())
		if doc == "" {
			doc = strings.TrimSpace(flat[i].Comment.Text())
		}
		if doc != "" {
			out[name+"."+jsonName] = doc
		}
	}
}

// reflectName is the type's name as reflect.Type.Name reports it — which is what
// the schema builder keys on at run time. For an instantiated generic that
// includes the type arguments, qualified by package path.
func reflectName(n *types.Named) string {
	name := n.Obj().Name()
	args := n.TypeArgs()
	if args == nil || args.Len() == 0 {
		return name
	}
	parts := make([]string, args.Len())
	for i := range parts {
		parts[i] = types.TypeString(args.At(i), func(p *types.Package) string { return p.Path() })
	}
	return name + "[" + strings.Join(parts, ",") + "]"
}

// file is the comment-bearing parse of the file holding pos, with pos as a LINE
// in it.
//
// The line, not the byte offset, is what identifies a declaration across the two
// views. A position in a package this pass loaded from source has both; a position
// in an IMPORTED package comes from export data, whose positions carry the real
// filename and line and a synthetic offset (the importer fabricates a file whose
// every line is one byte long). Matching on the offset therefore matched nothing
// the moment a type lived in another package — which is every op whose In and Out
// live in the call plane. Those ops had a description and ZERO field
// descriptions: every field-level projection of the plane was nameless.
func (e *extractor) file(pos token.Pos) (*ast.File, int) {
	p := e.load.Position(pos)
	if p.Filename == "" {
		return nil, 0
	}
	f, ok := e.files[p.Filename]
	if !ok {
		f, _ = parser.ParseFile(e.own, p.Filename, nil, parser.ParseComments|parser.SkipObjectResolution)
		e.files[p.Filename] = f
	}
	return f, p.Line
}

func (e *extractor) line(pos token.Pos) int { return e.own.Position(pos).Line }

// funcDecl is the declaration of the function whose name is at pos — in this
// package or any other, since a handler is as often a package away.
func (e *extractor) funcDecl(pos token.Pos) *ast.FuncDecl {
	f, line := e.file(pos)
	if f == nil {
		return nil
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && e.line(fd.Name.Pos()) == line {
			return fd
		}
	}
	return nil
}

// typeSpec is the declaration of the type named `name` at pos. The name is
// checked as well as the line because `type ( A struct{}; B struct{} )` puts two
// declarations on one line, and prose filed under the wrong type is worse than
// none.
func (e *extractor) typeSpec(pos token.Pos, name string) *ast.TypeSpec {
	f, line := e.file(pos)
	if f == nil {
		return nil
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name == name && e.line(ts.Name.Pos()) == line {
				return ts
			}
		}
	}
	return nil
}

// commentAbove is the comment block immediately above the line at pos.
func (e *extractor) commentAbove(pos token.Pos) string {
	f, _ := e.file(pos)
	if f == nil {
		return ""
	}
	want := e.load.Position(pos).Line - 1
	for _, g := range f.Comments {
		if e.own.Position(g.End()).Line == want {
			return g.Text()
		}
	}
	return ""
}

// splitDoc separates the prose from the Example:/Response: bodies.
//
// The bodies are part of the comment for the same reason the prose is: one
// place. A body may run onto the following lines — it is read until it parses —
// and is compacted, so the generated file is stable whatever the source
// formatting. Malformed JSON is an error, never a spec that ships broken.
func splitDoc(text string) (prose, example, response string, err error) {
	lines := strings.Split(text, "\n")
	var keep []string
	for i := 0; i < len(lines); i++ {
		key, rest, ok := marker(lines[i])
		if !ok {
			keep = append(keep, lines[i])
			continue
		}
		body := rest
		for j := i + 1; j < len(lines) && !json.Valid([]byte(body)); j++ {
			if _, _, isMarker := marker(lines[j]); isMarker {
				break
			}
			body += "\n" + lines[j]
			i = j
		}
		compact, cerr := compactJSON(body)
		if cerr != nil {
			return "", "", "", fmt.Errorf("%s: %w", key, cerr)
		}
		if key == "Example" {
			example = compact
		} else {
			response = compact
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n")), example, response, nil
}

// marker recognises an "Example:" or "Response:" line and returns what follows.
func marker(line string) (key, rest string, ok bool) {
	for _, k := range [...]string{"Example", "Response"} {
		if r, found := strings.CutPrefix(strings.TrimSpace(line), k+":"); found {
			return k, strings.TrimSpace(r), true
		}
	}
	return "", "", false
}

// compactJSON validates and normalises one body. Compaction (rather than a
// decode/re-encode round trip) keeps the author's key order, and rejects
// trailing garbage as loudly as it rejects a missing brace.
func compactJSON(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("no JSON body")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	return buf.String(), nil
}
