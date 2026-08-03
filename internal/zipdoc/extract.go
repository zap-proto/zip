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
	"go/doc"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"

	"github.com/zap-proto/zip"
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

// Package is one loaded package, the operations registered in it, and what its
// package doc comment says about the product it implements.
type Package struct {
	Dir  string // directory the generated file belongs in
	Name string // package clause
	Path string // import path, so a package inside zip itself skips the import
	Ops  []Op
	Meta zip.Meta
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
		meta, err := e.meta(p)
		if err != nil {
			return nil, err
		}
		out = append(out, Package{
			Dir:  filepath.Dir(files[0]),
			Name: p.Name,
			Path: p.PkgPath,
			Ops:  ops,
			Meta: meta,
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
			if !found {
				op, found = e.raw(p.TypesInfo, call, prefixes)
			}
			if found {
				ops = append(ops, op)
			} else {
				ops = append(ops, e.alias(p.TypesInfo, call, prefixes)...)
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
	//
	// A router that ARRIVED as a parameter — `func Mount(app zip.Router, …)`, the
	// shape a subsystem uses to be mounted by its host — cannot be resolved here:
	// only the caller knows the prefix, and that call is in another package. Read
	// as mounted at the root, and ONLY for a path already written ABSOLUTE, which
	// is the same rule and the same justification the raw case has always used:
	// zip.Describe is keyed by "METHOD /path" and renders only where a router
	// carries that key, so a guess that is wrong renders NOWHERE — the same
	// silence as not lifting it, never a sentence on the wrong operation.
	//
	// Refusing instead cost the whole prose surface of every subsystem written
	// that way. Measured on hanzoai/cloud: 46 of 93 packages could not run this
	// pass at all, so each published operationIds and silence in the document, the
	// MCP tool list, the CLI's help and eight SDKs — and the sentences were sitting
	// in the source the whole time.
	//
	// A RELATIVE path with an unresolvable router is still an error. There the
	// address genuinely cannot be composed, and a typed op filed under an identity
	// this pass invented is the hole in the schema surface the strictness is for.
	prefix, perr := routerPrefix(info, prefixes, call.Args[0])
	if perr != nil {
		if !strings.HasPrefix(path, "/") {
			return Op{}, false, fmt.Errorf("%s: zip.%s: %w", e.load.Position(call.Args[0].Pos()), fn.Name(), perr)
		}
		prefix = ""
	}
	path = joinPath(prefix, path)

	doc := e.handlerDoc(info, call, call.Args[2])
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

// raw reads one router.Get("/path", handler) — a registration the wire keeps
// UNTYPED — into a prose-only Op.
//
// Some routes cannot become typed ops and it is the wire, not the author, that
// says so: an OIDC redirect, a JWKS document, a SCIM body governed by RFC 7643, a
// multipart form, an SSE stream. They are real operations that a paying caller
// reaches, and until now they had nowhere at all to state what they do, so every
// one of them published an address and silence.
//
// What is lifted is only the DESCRIPTION. There is no In and no Out to walk, so
// no schemas and no field prose — which is exactly right: this pass must never
// let an untyped route look typed. It also registers no route and invents no
// operation; zip.Describe is keyed by "METHOD /path" and renders only where a
// router already carries that address, so the router stays the sole authority on
// what exists and this adds only what it MEANS.
//
// A registration whose path is not a constant is skipped rather than refused,
// unlike the typed case. A typed op with a computed path is a hole in the schema
// surface and must stop the build; an untyped one is prose that cannot be filed,
// and refusing there would make every raw handler in the fleet a build error the
// day this shipped.
func (e *extractor) raw(info *types.Info, call *ast.CallExpr, prefixes map[types.Object]string) (Op, bool) {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || len(call.Args) < 2 {
		return Op{}, false
	}
	method, ok := verbs[sel.Sel.Name]
	if !ok {
		return Op{}, false
	}
	// The RECEIVER decides: a *zip.App or anything satisfying zip.Router is a
	// route table. Matching on the method name alone would claim every Get in
	// every package in the fleet.
	if !isZipRouter(info.Types[sel.X].Type) {
		return Op{}, false
	}
	lit := info.Types[call.Args[0]].Value
	if lit == nil || lit.Kind() != constant.String {
		return Op{}, false
	}
	// A group assigned in this file resolves exactly. A router that ARRIVED as a
	// parameter — `func routeFrontDoor(r zip.Router, …)`, the shape a subsystem
	// uses to be mounted by its host — cannot: only the caller knows the prefix,
	// and that call is usually in another file or another package.
	//
	// So an unresolvable router is read as mounted at the root, and ONLY for a
	// path already written absolute. Getting that wrong costs nothing a reader can
	// see: zip.Describe renders only where the router carries the key, so prose
	// filed under a path nothing serves renders nowhere — the same silence as not
	// lifting it, never a sentence attached to the wrong operation. The upside is
	// the whole pre-auth surface of every subsystem written this way, which is
	// otherwise the single largest undescribed set in the fleet.
	prefix, err := routerPrefix(info, prefixes, sel.X)
	if err != nil {
		if !strings.HasPrefix(constant.StringVal(lit), "/") {
			return Op{}, false
		}
		prefix = ""
	}
	doc := e.handlerDoc(info, call, call.Args[1])
	prose, example, response, derr := splitDoc(doc)
	if derr != nil || strings.TrimSpace(prose) == "" {
		return Op{}, false
	}
	return Op{
		Method:      method,
		Path:        joinPath(prefix, constant.StringVal(lit)),
		Description: prose,
		Example:     example,
		Response:    response,
		Fields:      map[string]string{},
	}, true
}

// alias reads one zip.Alias(router.Get, canonical, legacy, handler) into TWO
// prose-only Ops carrying the same sentence.
//
// Both addresses serve one handler, so both mean the same thing and both must say
// so — a consumer that reads the document is as likely to arrive at the legacy
// spelling as the canonical one, and finding silence there is exactly the defect
// a legacy alias exists to avoid.
//
// A registration made inside a helper is invisible to a pass that reads
// router.Get(path, handler) calls, which is why zip owns Alias: this matcher can
// only recognise a function whose identity it knows.
func (e *extractor) alias(info *types.Info, call *ast.CallExpr, prefixes map[types.Object]string) []Op {
	id := calleeIdent(call.Fun)
	if id == nil || len(call.Args) < 4 {
		return nil
	}
	fn, _ := info.Uses[id].(*types.Func)
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != ZipPkg || fn.Name() != "Alias" {
		return nil
	}
	// The registrar is a METHOD VALUE — `r.Get` — so it names both the verb and
	// the router the paths hang off.
	sel, ok := ast.Unparen(call.Args[0]).(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	method, ok := verbs[sel.Sel.Name]
	if !ok || !isZipRouter(info.Types[sel.X].Type) {
		return nil
	}
	prefix, err := routerPrefix(info, prefixes, sel.X)
	if err != nil {
		prefix = ""
	}
	doc := e.handlerDoc(info, call, call.Args[3])
	prose, example, response, derr := splitDoc(doc)
	if derr != nil || strings.TrimSpace(prose) == "" {
		return nil
	}
	var out []Op
	for _, arg := range call.Args[1:3] {
		lit := info.Types[arg].Value
		if lit == nil || lit.Kind() != constant.String {
			continue
		}
		p := constant.StringVal(lit)
		if prefix == "" && !strings.HasPrefix(p, "/") {
			continue
		}
		out = append(out, Op{
			Method:      method,
			Path:        joinPath(prefix, p),
			Description: prose,
			Example:     example,
			Response:    response,
			Fields:      map[string]string{},
		})
	}
	return out
}

// isZipRouter reports whether t is a route table this pass may read: *zip.App,
// or any type zip.Router names (a group, an app, whatever a host hands down).
func isZipRouter(t types.Type) bool {
	if t == nil {
		return false
	}
	if isZipApp(t) {
		return true
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == ZipPkg && named.Obj().Name() == "Router"
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
// exactly the silent drop this resolution exists to end.
//
// So it never ASSUMES a prefix: an unresolvable router is an error naming the
// call. What the caller does with that error is the caller's rule, and both
// callers make the same one — a path already written absolute is read as
// root-mounted, a relative one is refused (see [extractor.call]).
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

// handlerDoc is the comment that documents the operation. A handler reaches a
// registration in exactly three shapes, and each has one place its prose can be
// written:
//
//	zip.Get(app, p, ListUsers)      // named    → the function's doc comment
//	zip.Get(app, p, listUsers(db))  // built    → the BUILDER's doc comment
//	zip.Get(app, p, func(…) {…})    // inline   → the comment above the call
//
// The middle shape is the one a service reaches for the moment a handler needs a
// dependency: a function that closes over db and returns the handler. The value
// it returns is a closure with no declaration of its own, so the builder is not
// merely where the prose is CONVENIENT — it is the only declaration there is.
// Reading the callee is therefore the same rule as the named case, applied one
// call deep, and not a second convention: in all three the sentence sits
// immediately above the code that does the work.
//
// Nothing about this was a style preference. Before it, `// listProviders returns
// every provider in the owner scope, newest first.` — already written, already
// reviewed, sitting on the builder — reached no document at all, and the
// operation published an operationId and silence.
func (e *extractor) handlerDoc(info *types.Info, call *ast.CallExpr, arg ast.Expr) string {
	switch h := ast.Unparen(arg).(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return e.funcDoc(info, calleeIdent(h))
	case *ast.CallExpr:
		return e.funcDoc(info, calleeIdent(h.Fun))
	case *ast.FuncLit:
		// An inline handler names no function, so only the sentence-shape
		// evidence can apply — and it must, because the comment above the
		// registration is written to the same convention.
		//
		// The comment is looked for above the REGISTRATION, not above the
		// closure. The two are the same line only while the whole call fits on
		// one; the moment options push the handler onto its own line — which is
		// what any registration carrying WithSummary/WithTags looks like — the
		// line above the closure is the `zip.Post(app, path,` line, and every
		// sentence written above such a call was silently dropped.
		return stripSelf(e.commentAbove(call.Pos()), "")
	}
	return ""
}

// funcDoc is the doc comment of the function id names, with the leading symbol
// stripped: godoc wants "ListUsers returns …" and a consumer reading the help
// for this operation wants "Returns …".
func (e *extractor) funcDoc(info *types.Info, id *ast.Ident) string {
	if id == nil {
		return ""
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return ""
	}
	d := e.funcDecl(fn.Pos())
	if d == nil {
		return ""
	}
	return stripSelf(d.Doc.Text(), fn.Name())
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

// stripSelf drops the leading identifier Go's doc convention requires — "RevokeKey
// revokes …" — when the comment is projected OUT of Go's namespace, where the
// function name is noise: an OpenAPI description, its derived summary, an MCP tool
// description, a CLI help line. The source stays idiomatic Go; the document reads
// as prose. One transform, here, because every projection downstream inherits
// this text.
//
// TWO EVIDENCES, one law, strongest first.
//
// The handler's own NAME is exact and needs no judgment. It is also, measured
// across 829 lifted comments in one fleet, the minority case: 449 of them opened
// with a symbol that is not the handler's identifier at all — `deleteLB` carrying
// "DeleteLoadBalancer removes one of the caller org's load balancers", `list`
// carrying "ListAgents returns every agent". The convention is being followed
// against a CONCEPTUAL name, so an exact-match rule leaves the majority of the
// leak in place, and every one of those sentences reached an SDK docstring, an
// MCP tool and a CLI help line opening with a Go symbol the reader cannot see,
// cannot call, and already has in operationId.
//
// So failing an exact match, Go's own SENTENCE SHAPE is the evidence: a strict
// CamelCase word followed by a lowercase verb, which is the form the convention
// itself prescribes. Both halves are deliberately narrow, because a wrong strip
// damages a sentence somebody wrote while a missed one leaves it as it was:
//
//   - strict CamelCase means two or more humps, each an initial capital followed
//     by lowercase (ListAgents, D1DatabaseList). A run of capitals is not a hump,
//     so GPUs, OpenAPI and HTTPHandler stay the ordinary words they are.
//   - the verb is a plain lowercase word and never a copula. "ListAgents returns
//     every agent" is a symbol and its verb; "CompleteDeployment is the CI
//     completion hook" is a sentence ABOUT a name, and dropping it would leave
//     "Is the CI completion hook".
//
// Anything else is left exactly as written.
func stripSelf(text, name string) string {
	if rest, ok := strings.CutPrefix(text, name+" "); ok && name != "" {
		return recapitalize(rest, text)
	}
	word, rest, spaced := strings.Cut(text, " ")
	if !spaced || !camelCase(word) {
		return text
	}
	verb, _, _ := strings.Cut(rest, " ")
	if !lowerWord(verb) || copula[verb] {
		return text
	}
	return recapitalize(rest, text)
}

// copula is the verb that makes the leading word the sentence's SUBJECT rather
// than the symbol being documented.
var copula = map[string]bool{"is": true, "are": true, "was": true, "were": true}

// recapitalize starts rest with a capital, or answers whole when it cannot.
func recapitalize(rest, whole string) string {
	r, size := utf8.DecodeRuneInString(rest)
	if r == utf8.RuneError {
		return whole
	}
	return string(unicode.ToUpper(r)) + rest[size:]
}

// camelCase reports whether w is strict CamelCase: two or more humps, each an
// initial capital followed by at least one lowercase letter or digit. ASCII only,
// which is the whole of what a Go identifier can be.
func camelCase(w string) bool {
	humps := 0
	for i := 0; i < len(w); humps++ {
		if w[i] < 'A' || w[i] > 'Z' {
			return false
		}
		tail := i + 1
		for tail < len(w) && (w[tail] >= 'a' && w[tail] <= 'z' || w[tail] >= '0' && w[tail] <= '9') {
			tail++
		}
		if tail == i+1 {
			return false // a bare capital, or a run of them: not a hump
		}
		i = tail
	}
	return humps >= 2
}

// lowerWord reports whether w is a plain lowercase word — no punctuation, so a
// verb trailed by a comma or a parenthesis is not one and nothing is stripped.
func lowerWord(w string) bool {
	if w == "" {
		return false
	}
	for i := 0; i < len(w); i++ {
		if w[i] < 'a' || w[i] > 'z' {
			return false
		}
	}
	return true
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
func marker(line string) (key, rest string, ok bool) { return mark(line, "Example", "Response") }

// mark recognises a "Key: value" line for one of keys. ONE matcher for both
// grammars — the operation's Example/Response bodies and the package's product
// facts — so a marker is recognised the same way wherever it is written, and
// alignment (`Product:    Hanzo Vector`) is not a second dialect.
//
// Only the named keys match. Anything else is prose, exactly as it has always
// been: a package doc is full of lines that open with a capitalised word and a
// colon, and a matcher that claimed them would silently eat somebody's sentence.
func mark(line string, keys ...string) (key, rest string, ok bool) {
	line = strings.TrimSpace(line)
	for _, k := range keys {
		if r, found := strings.CutPrefix(line, k+":"); found {
			return k, strings.TrimSpace(r), true
		}
	}
	return "", "", false
}

// catalogKeys are the marker names a package doc may declare, derived from
// [zip.Meta] itself rather than listed here — a field added to the type is a
// marker the grammar accepts, with no second list to update and no way for the
// two to disagree. Description is excluded: it is the package's own sentence,
// read from the prose, and not something a marker states.
var catalogKeys = func() []string {
	t := reflect.TypeOf(zip.Meta{})
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); f.Name != "Description" {
			out = append(out, f.Name)
		}
	}
	return out
}()

// meta is what the package says about the product it implements: its synopsis
// and its catalog markers.
//
// THE CONVENTION IS THE WHOLE TEST. Go's package doc is the leading comment that
// opens "Package …", and only that comment is read — go/doc's fallback (the
// first file in filename order carrying any leading comment) is wrong here often
// enough to matter, because a file that opens with a note ABOUT THAT FILE would
// then be published as the product's description. A caller cannot tell an
// undescribed product from a misfiled one.
func (e *extractor) meta(p *packages.Package) (zip.Meta, error) {
	for _, f := range p.Syntax {
		if f.Doc == nil {
			continue
		}
		text := f.Doc.Text()
		if !strings.HasPrefix(text, "Package ") {
			continue
		}
		m, err := parseMeta(text)
		if err != nil {
			return zip.Meta{}, fmt.Errorf("%s: package %s: %w", e.load.Position(f.Doc.Pos()), p.PkgPath, err)
		}
		return m, nil
	}
	return zip.Meta{}, nil
}

// parseMeta splits a package doc comment into its product facts and its prose.
//
// The prose keeps its synopsis — the first sentence, which is what a menu, a tag
// description and an app's `info` block each want — and the markers are lifted
// out of it, so a declaration never reads back as part of the sentence.
//
// A key stated twice is refused. Two answers to one question is not a formatting
// slip; whichever the parser happened to keep would become the fact, and nothing
// downstream could see the other.
func parseMeta(text string) (zip.Meta, error) {
	var m zip.Meta
	v := reflect.ValueOf(&m).Elem()
	seen := map[string]bool{}
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		key, val, ok := mark(line, catalogKeys...)
		if !ok {
			keep = append(keep, line)
			continue
		}
		if seen[key] {
			return zip.Meta{}, fmt.Errorf("%s is declared twice; one question has one answer", key)
		}
		seen[key] = true
		f := v.FieldByName(key)
		if f.Kind() != reflect.String {
			return zip.Meta{}, fmt.Errorf("%s is not a string field of zip.Meta, so this grammar cannot carry it", key)
		}
		f.SetString(val)
	}
	m.Description = doc.Synopsis(strings.TrimSpace(strings.Join(keep, "\n")))
	if err := m.Valid(); err != nil {
		return zip.Meta{}, err
	}
	return m, nil
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
