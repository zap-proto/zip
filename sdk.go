package zip

// The Go SDK — the EIGHTH projection, and the only one that is source code.
//
// The same typed-op registry that produces the REST routes, the OpenAPI
// document, the MCP tools, the CLI, the op-call plane, the GraphQL schema and
// the ZAP IDL also produces a Go package that CALLS those ops: one method per
// operation, its In and Out declared as Go types, the call itself [Call] over
// ZAP. Nothing about it is written down anywhere — registering a typed op adds
// a method, and there is no second place for a client to drift in.
//
// # Why this is generated when [Call] is not
//
// [Call] is the right answer INSIDE the fleet, where the caller imports the op's
// In and Out from the package that declared them. A published SDK cannot: its
// users do not link the service, and linking it to reach three types drags in
// the service's whole dependency graph. So the types are RESTATED here, and the
// methods around them rendered rather than written. The emitted package depends
// on zip and nothing else.
//
// # Why the registry and not the document
//
// Because the document describes the JSON edge, and this wire is not JSON. A Go
// type has three shapes on the JSON edge that it does not have on the ZAP wire,
// and every one of them silently moves a field:
//
//	order       properties are a JSON object, and an object has no order.
//	            [LayoutOf] is declaration order, so a client that read the
//	            document would lay its fields alphabetically and read every
//	            value after the first from the wrong offset.
//	json:"-"    absent from the document, and STILL A SLOT on the wire.
//	embedding   an untagged embedded struct is FLATTENED into the document and
//	            NESTED on the wire — one property list, two shapes.
//
// So the generated struct is the declared struct, field for field, in order,
// with only the type names changed. The layout is then identical by
// construction rather than by a rule that has to be maintained. The document
// keeps the jobs it is the authority on — prose, the browser edge, the CLI —
// and [ZAPSchema] keeps the one only it can do, which is refusing.

import (
	"bytes"
	"fmt"
	"go/format"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/zap-proto/zip/internal/zapenc"
)

// SDK is one generated Go package: the source, and the ops that are not in it.
//
// Gaps is not decoration. An operation named there has NO method in Source, and
// a generator that emitted one anyway — with the field it could not express
// quietly dropped — would publish a client that compiles and lies. Same rule as
// [Schema.Gaps], for the same reason, and mostly the same list: an op the wire
// refuses is an op no client can call.
type SDK struct {
	Package string
	Source  []byte
	Gaps    []Gap
}

// Ops is how many operations the package can call.
func (s *SDK) Ops() int {
	return strings.Count(string(s.Source), "\nfunc (c *Client) ") - clientMethods
}

// clientMethods is how many methods [clientPreamble] declares on Client, which
// are not operations.
const clientMethods = 2

// SDK renders a Go package under pkg whose methods are this app's operations.
//
// The emitted package imports zip and nothing else, so it compiles wherever zip
// does — the point of restating the types rather than importing them.
func (a *App) SDK(pkg string) (*SDK, error) {
	if !validIdent(pkg) {
		return nil, fmt.Errorf("zip: %q is not a Go package name", pkg)
	}
	g := &render{
		sdk:   &SDK{Package: pkg},
		named: map[reflect.Type]string{},
		why:   map[reflect.Type]Gap{},
		taken: map[string]bool{},
		fld:   map[reflect.Type]map[string]string{},
		wire:  map[reflect.Type]string{},
		std:   map[string]bool{},
	}

	ops := append([]*registeredOp(nil), a.Registry()...)
	sort.Slice(ops, func(i, j int) bool { return opName(ops[i]) < opName(ops[j]) })
	for _, op := range ops {
		g.method(op)
	}
	if err := g.codecs(); err != nil {
		return nil, err
	}

	src, err := format.Source(g.render(a))
	if err != nil {
		return nil, fmt.Errorf("zip: generated source does not compile: %w", err)
	}
	g.sdk.Source = src
	sort.Slice(g.sdk.Gaps, func(i, j int) bool {
		if g.sdk.Gaps[i].Op != g.sdk.Gaps[j].Op {
			return g.sdk.Gaps[i].Op < g.sdk.Gaps[j].Op
		}
		return g.sdk.Gaps[i].Field < g.sdk.Gaps[j].Field
	})
	return g.sdk, nil
}

// ---- rendering --------------------------------------------------------------

// call is one operation, resolved: the Go names its method needs.
type call struct {
	id     string // the operationId — what Call addresses
	method string // the Go method name
	doc    Doc
	hasDoc bool
	in     string // the In type's Go name, "" for an op that takes nothing
	out    string // the Out type's Go name, "" for an op that answers nothing
}

type render struct {
	sdk   *SDK
	named map[reflect.Type]string // type → the name declared for it, "" while claiming
	// why is the refusal remembered for a type that has none. Without it the
	// SECOND op reaching a refused type is dropped in silence: the memo below
	// answers "no" and says nothing, so the op has no method and no gap, and
	// the only way to notice is to count the methods.
	why   map[reflect.Type]Gap
	taken map[string]bool // every name claimed at package scope
	calls []call
	decls []string
	// order is the types declared, in the order decls holds them, so a codec
	// can be written beside the struct it states rather than in a second half
	// of the file nobody reads next to the fields.
	order []reflect.Type
	// fld is what this package calls each declared type's fields. They are the
	// declared names, with one exception that matters: an embedded field IS its
	// type's name, and this package renamed the type.
	fld map[reflect.Type]map[string]string
	// wire is the codec emitted for a type that states its own bytes, and std
	// the standard-library imports those codecs need.
	wire map[reflect.Type]string
	std  map[string]bool
}

// method resolves one operation. An op whose In or Out cannot cross this wire
// gets no method: a client that could be called and could never succeed is
// worse than one that says the operation is not reachable.
func (g *render) method(op *registeredOp) {
	id := opName(op)
	doc, hasDoc := docFor(op.Pkg, op.Method, op.Path)
	c := call{id: id, method: exportIdent(id), doc: doc, hasDoc: hasDoc}
	if c.method == "" {
		g.gap(id, "", "", causeUnnamed)
		return
	}
	in, ok := g.declare(op.InType, id, docFields(hasDoc, doc))
	if !ok {
		return
	}
	out, ok := g.declare(op.OutType, id, docFields(hasDoc, doc))
	if !ok {
		return
	}
	c.in, c.out = in, out
	g.calls = append(g.calls, c)
}

// declare names the Go type for t, emitting a struct declaration the first time
// it is reached. It reports false when t has no wire form at all, having said
// why — the same refusal [ZAPSchema] makes, read from the same derivation.
func (g *render) declare(t reflect.Type, op string, fields map[string]string) (string, bool) {
	t = deref(t)
	if t == nil || t.Kind() != reflect.Struct || t.NumField() == 0 {
		// A void input or output. It is a type parameter and never a value, so
		// there is nothing to declare and nothing missing.
		return "", true
	}
	if name, seen := g.named[t]; seen {
		if name == "" {
			// Refused before, by another op. Say so again, against this one.
			had := g.why[t]
			g.gap(op, had.Field, had.Go, had.Cause)
			return "", false
		}
		return name, true
	}
	shape, err := zapenc.LayoutOf(t)
	if err != nil {
		return "", g.refuse(t, op, goName(t), t.String(), causeOf(err))
	}
	// A fixed array — every id in the fleet — is where this used to stop. The
	// REFLECTIVE encoder refuses bytes_fixed[N] outright, deliberately, so that
	// an id forces its type to state its own wire (see [Wire]); and the SDK read
	// that refusal as a fact about the op and reported a gap.
	//
	// It is a fact about the ENCODER, not about the op. The layout is right — an
	// offset and a width — and [Codecs] already writes the two methods that
	// carry it, inline, from those same offsets. The struct written just below
	// restates the declared type field for field and in order, so its layout is
	// the declared type's layout; a codec stating those offsets against these
	// names is the same wire, spelled in this package. So the SDK writes one
	// (see [render.codecs]) instead of refusing, and an id crosses.
	//
	// The name is claimed BEFORE the fields are walked. That claim is the cycle
	// guard, and it is why a type that reaches itself through a pointer or a
	// slice declares once instead of recursing — the same rule the document's
	// schema registry follows one layer up.
	// A type keeps its own name. The generated package is the namespace, so the
	// service's package is not part of it — until two packages both declare a
	// Config, and then the qualifier is the honest distinction rather than an
	// ordinal nobody can read.
	name := exportIdent(t.Name())
	if name == "" || g.taken[name] {
		name = exportIdent(goName(t))
	}
	name = g.claim(name)
	g.named[t] = name

	var b strings.Builder
	if d := fields[t.Name()]; d != "" {
		prose(&b, name, d)
	}
	fmt.Fprintf(&b, "type %s struct {\n", name)
	// wrote is the declared name of each field this package restates, in the
	// order it restates them, and spelled is what it calls each one.
	var wrote []string
	spelled := map[string]string{}
	// Declaration order, exported fields only — exactly what buildLayout walks.
	// Anything else here is a different layout wearing the same name.
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if d := fields[t.Name()+"."+jsonFieldName(f)]; d != "" {
			prose(&b, "", d)
		}
		at := t.Name() + "." + f.Name
		goType, ok := g.typeOf(f.Type, op, at, fields)
		if !ok {
			// A field that cannot cross refuses the type that holds it, which
			// refuses the op. Substituting an empty struct here instead — which
			// is what this did — emitted a client that compiles, calls, and
			// arrives with the value gone. Reported once, at the field.
			return "", g.refuse(t, op, at, f.Type.String(), causeNested)
		}
		if f.Anonymous {
			// An embedded field IS its type's name, and the wire cares: it takes
			// one nested slot, where the flattened form would take several. A
			// spelling that is not a name — the empty struct an unnameable type
			// renders as — is not Go there, so the type is refused rather than
			// written out unparseable.
			if !isIdent(goType) {
				return "", g.refuse(t, op, at, f.Type.String(), causeEmbedded)
			}
			fmt.Fprintf(&b, "\t%s %s\n", goType, tagOf(f))
			// The field's name here is the TYPE's name, which this package just
			// renamed — so a codec reaching it must reach it by the new one.
			wrote, spelled[f.Name] = append(wrote, f.Name), goType
			continue
		}
		fmt.Fprintf(&b, "\t%s %s %s\n", f.Name, goType, tagOf(f))
		wrote, spelled[f.Name] = append(wrote, f.Name), f.Name
	}
	b.WriteString("}\n")

	// The struct just written and the layout just read have to be the same
	// sequence of fields, because the codec states the layout's OFFSETS against
	// these NAMES. It holds by construction — both walk the exported fields in
	// declaration order — and is checked anyway, because the failure it would
	// catch is every field after the first one that moved being read from the
	// wrong place, with nothing failing. A guessed layout is worse than a gap.
	if len(wrote) != len(shape.Slots) {
		return "", g.refuse(t, op, goName(t), t.String(), causeCodec)
	}
	for i, s := range shape.Slots {
		if wrote[i] != s.Name {
			return "", g.refuse(t, op, goName(t)+"."+s.Name, s.Type, causeCodec)
		}
	}
	g.fld[t] = spelled
	g.decls = append(g.decls, b.String())
	g.order = append(g.order, t)
	return name, true
}

// typeOf RESOLVES one field's type: it declares every struct the field reaches
// and reports whether the field can cross at all. What the type is CALLED here
// is [render.spell]'s answer, so the struct declarations and the codecs beside
// them cannot spell the same type two ways.
func (g *render) typeOf(t reflect.Type, op, at string, fields map[string]string) (string, bool) {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		if _, ok := g.typeOf(t.Elem(), op, at, fields); !ok {
			return "", false
		}
	case reflect.Map:
		// A map has no layout: a key set is not a set of offsets. LayoutOf has
		// already refused the struct that holds it, so this only resolves where
		// the map sits behind something the wire never reaches.
		_, kok := g.typeOf(t.Key(), op, at, fields)
		_, eok := g.typeOf(t.Elem(), op, at, fields)
		if !kok || !eok {
			return "", false
		}
	case reflect.Struct:
		if t.NumField() > 0 {
			if _, ok := g.declare(t, op, fields); !ok {
				return "", false
			}
		}
	case reflect.Interface:
		// An interface field names no type, so there is nothing to declare and
		// nothing that could cross. It refuses the type that holds it.
		g.gap(op, at, t.String(), CauseAny)
		return "", false
	}
	return g.spell(t), true
}

// spell is one type as Go source in this package: the shape it has in the
// service, with every struct it reaches renamed to the declaration made here.
//
// It answers for the struct fields and for the codecs — which is why it is a
// function and not a line inside [render.typeOf]. A codec that spelled a type
// its own way would compile and read the field back as something else.
func (g *render) spell(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + g.spell(t.Elem())
	case reflect.Slice:
		return "[]" + g.spell(t.Elem())
	case reflect.Array:
		return "[" + strconv.Itoa(t.Len()) + "]" + g.spell(t.Elem())
	case reflect.Map:
		return "map[" + g.spell(t.Key()) + "]" + g.spell(t.Elem())
	case reflect.Struct:
		if t.NumField() == 0 {
			return "struct{}"
		}
		return g.named[t]
	}
	// A basic kind keeps its own spelling — including a named one's underlying
	// type, because the name belongs to the service's package and the width is
	// what the wire reads.
	return t.Kind().String()
}

// name and field are the other two halves of [naming]: what this package calls
// a declared type, and what it calls one of its fields. Together with spell they
// are how [Codecs]' emitter writes a codec into a package that did not declare
// the types it states.
func (g *render) name(t reflect.Type) string { return g.named[t] }

func (g *render) field(t reflect.Type, declared string) string {
	if f, ok := g.fld[t][declared]; ok {
		return f
	}
	return declared
}

// codecs writes the wire for every declared type that has to state its own.
//
// A fixed array is the reason any of them do: the reflective encoder refuses
// bytes_fixed[N], so a value holding one crosses only when its type answers for
// its own bytes. The set is not just those types. It is closed UPWARD, because a
// parent encoded reflectively reflects over its children too — a nested codec is
// reached by the parent's METHOD and never by the reflective walk — and closed
// DOWNWARD, because a codec calls MarshalZAP on every value it nests.
//
// So one id anywhere under an op's In or Out states the wire for that whole
// tree, and a tree with no id in it keeps the derived wire it already had. That
// is the point of drawing the line here rather than at "every type": the ops
// that already crossed are not re-encoded to fix the ones that could not.
func (g *render) codecs() error {
	kids, fixed := g.nesting()

	// wants is "this type's tree holds a fixed array". LayoutOf refuses a type
	// that contains itself, so the nesting is acyclic and the walk terminates.
	memo := map[reflect.Type]bool{}
	var wants func(reflect.Type) bool
	wants = func(t reflect.Type) bool {
		if v, ok := memo[t]; ok {
			return v
		}
		v := fixed[t]
		for _, k := range kids[t] {
			if wants(k) {
				v = true
			}
		}
		memo[t] = v
		return v
	}

	coded := map[reflect.Type]bool{}
	var mark func(reflect.Type)
	mark = func(t reflect.Type) {
		if coded[t] {
			return
		}
		coded[t] = true
		for _, k := range kids[t] {
			mark(k)
		}
	}
	for _, t := range g.order {
		if wants(t) {
			mark(t)
		}
	}
	for _, t := range g.order {
		if !coded[t] {
			continue
		}
		src, err := g.codec(t)
		if err != nil {
			return err
		}
		g.wire[t] = src
	}
	return nil
}

// nesting is, for each declared type, the declared types it holds — through a
// pointer or a list — and whether its own layout has a fixed array in it.
//
// A value with no slots is not among the children: it crosses as a complete and
// empty message that its parent writes INLINE, which is what [Codecs] does with
// one, so there is no method to call and nothing to state.
func (g *render) nesting() (map[reflect.Type][]reflect.Type, map[reflect.Type]bool) {
	kids := map[reflect.Type][]reflect.Type{}
	fixed := map[reflect.Type]bool{}
	for _, t := range g.order {
		shape, err := zapenc.LayoutOf(t)
		if err != nil {
			continue
		}
		for _, s := range shape.Slots {
			if strings.HasPrefix(s.Type, "bytes_fixed[") || strings.HasPrefix(s.Elem, "bytes_fixed[") {
				fixed[t] = true
			}
			f, ok := held(t, s.Name)
			if !ok {
				continue
			}
			if n := nests(f.Type); n != nil && !empty(n) && g.named[n] != "" {
				kids[t] = append(kids[t], n)
			}
		}
	}
	return kids, fixed
}

// codec is one type's two methods, written by the same emitter [Codecs] uses —
// the same offsets, in the same order, through the same builder calls — with
// this package's names in place of the service's. Two emitters would be two
// wires, and the second one would be spoken by nobody.
func (g *render) codec(t reflect.Type) (string, error) {
	p := &pkg{
		alias: map[string]string{},
		taken: map[string]bool{},
		plain: map[string]bool{},
		as:    g,
	}
	var b bytes.Buffer
	if err := declare(&b, t, p); err != nil {
		return "", err
	}
	for q := range p.alias {
		g.std[q] = true
	}
	return b.String(), nil
}

// refuse records why a type has no wire form, remembers it for the next op to
// reach the same type, and reports false so the caller can stop.
func (g *render) refuse(t reflect.Type, op, field, goType, cause string) bool {
	g.named[t] = ""
	g.why[t] = Gap{Field: field, Go: goType, Cause: cause}
	g.gap(op, field, goType, cause)
	return false
}

// isIdent says whether a spelling can stand where Go wants a type name, which
// is the only thing an embedded field may be.
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// claim reserves one package-scope name, so a type called Client or Dial cannot
// displace the package's own declarations.
func (g *render) claim(name string) string {
	if name == "" {
		name = "T"
	}
	if goReserved[name] {
		name += "_"
	}
	base := name
	for n := 2; g.taken[name]; n++ {
		name = base + strconv.Itoa(n)
	}
	g.taken[name] = true
	return name
}

func (g *render) gap(op, field, goType, cause string) {
	g.sdk.Gaps = append(g.sdk.Gaps, Gap{Op: op, Field: field, Go: goType, Cause: cause})
}

// render assembles the file.
func (g *render) render(a *App) []byte {
	name := a.cfg.AppName
	if name == "" {
		name = g.sdk.Package
	}
	var b strings.Builder
	b.WriteString("// Code generated from the typed-op registry by zip. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "// Package %s calls %s's operations over ZAP.\n", g.sdk.Package, name)
	fmt.Fprintf(&b, "package %s\n\n", g.sdk.Package)
	// The import block is what the file uses and nothing else. A package with
	// no codec in it imports zip and context — restating the types is why it
	// needs no more — and one that states a wire also takes the builder that
	// writes it, plus whatever the reads reach for.
	std := []string{"context"}
	if len(g.wire) > 0 {
		std = append(std, "fmt")
	}
	for q := range g.std {
		std = append(std, q)
	}
	sort.Strings(std)
	b.WriteString("import (\n")
	for _, q := range std {
		fmt.Fprintf(&b, "\t%q\n", q)
	}
	b.WriteString("\n")
	if len(g.wire) > 0 {
		b.WriteString("\tzap \"github.com/zap-proto/go\"\n")
	}
	b.WriteString("\t\"github.com/zap-proto/zip\"\n)\n\n")

	// A type's codec is written beside the struct it states, not in a second
	// half of the file: the offsets and the fields they belong to are one thing
	// to read.
	for i, d := range g.decls {
		b.WriteString(d)
		b.WriteString("\n")
		if w := g.wire[g.order[i]]; w != "" {
			b.WriteString(w)
		}
	}
	b.WriteString(clientPreamble)

	for _, c := range g.calls {
		if c.hasDoc && c.doc.Description != "" {
			prose(&b, c.method, c.doc.Description)
		} else {
			fmt.Fprintf(&b, "// %s calls %s.\n", c.method, c.id)
		}
		arg, inType := "(*struct{})(nil)", "struct{}"
		params := ""
		if c.in != "" {
			arg, inType, params = "in", c.in, ", in *"+c.in
		}
		if c.out == "" {
			// A void op yields a nil *Out. The type parameter still has to be
			// something, and an empty struct is the type of "nothing came back".
			fmt.Fprintf(&b, "func (c *Client) %s(ctx context.Context%s) error {\n", c.method, params)
			fmt.Fprintf(&b, "\t_, err := zip.Call[%s, struct{}](ctx, c.conn, %q, %s)\n\treturn err\n}\n\n", inType, c.id, arg)
			continue
		}
		fmt.Fprintf(&b, "func (c *Client) %s(ctx context.Context%s) (*%s, error) {\n", c.method, params, c.out)
		fmt.Fprintf(&b, "\treturn zip.Call[%s, %s](ctx, c.conn, %q, %s)\n}\n\n", inType, c.out, c.id, arg)
	}
	return []byte(b.String())
}

// clientPreamble is the part of every generated package that is the same in
// every generated package: how you get one, and how you let it go. It is
// written out rather than shipped as a base type in zip so the emitted package
// has one dependency and nothing to inherit from.
const clientPreamble = `// Client calls this service's operations. It is safe for concurrent use and
// holds a pooled connection, so a warm Client costs a round trip and no dial.
type Client struct{ conn *zip.Conn }

// Dial returns a Client for the service at addr. The scheme selects the
// transport exactly as it does everywhere else — a bare path is ZAP over a unix
// socket and a bare host:port is ZAP over tcp.
func Dial(addr string) (*Client, error) {
	conn, err := zip.Dial(addr)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// Open returns a Client over a connection the caller already holds, so one
// process reaching several services opens one pool rather than one per package.
func Open(conn *zip.Conn) *Client { return &Client{conn: conn} }

// Conn is the connection this Client calls over.
func (c *Client) Conn() *zip.Conn { return c.conn }

// Close releases the pooled connections. The Client stays usable; a later call
// redials.
func (c *Client) Close() error { return c.conn.Close() }

`

// prose writes a doc comment, naming the declaration on the first line so godoc
// reads it as its own.
func prose(b *strings.Builder, name, d string) {
	lines := strings.Split(strings.TrimSpace(d), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if i == 0 && name != "" {
			fmt.Fprintf(b, "// %s %s\n", name, strings.TrimSpace(strings.TrimPrefix(line, name+" ")))
			continue
		}
		if line == "" {
			b.WriteString("//\n")
			continue
		}
		fmt.Fprintf(b, "// %s\n", line)
	}
}

func tagOf(f reflect.StructField) string {
	if f.Tag == "" {
		return ""
	}
	return "`" + string(f.Tag) + "`"
}

// causeOf reads a layout refusal back into the [Gap] vocabulary, so one
// `sort | uniq -c` over the causes ranks what to fix.
func causeOf(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "contains itself"):
		return CauseUnwirable
	case strings.Contains(msg, "map"):
		return CauseMap
	case strings.Contains(msg, "interface"):
		return CauseAny
	}
	return CauseUnwirable
}

// ---- naming -----------------------------------------------------------------

// exportIdent renders a wire name as an exported Go identifier. The parts of a
// name are its runs of letters and digits, and everything else is a separator,
// so get_platform_height, platform.getHeight and platform-get-height all reach
// PlatformGetHeight — one operation has one method name however its id was
// spelled.
//
// The rest of each part is left alone, which is what carries an initialism
// through: nodeID becomes NodeID, not NodeId.
func exportIdent(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			if up {
				b.WriteRune(upper(r))
				up = false
				continue
			}
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if b.Len() == 0 {
				// A Go identifier cannot start with a digit, and dropping the
				// digit would make 2fa and fa the same name.
				b.WriteByte('N')
			}
			b.WriteRune(r)
			up = false
		default:
			up = true
		}
	}
	return b.String()
}

func upper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - ('a' - 'A')
	}
	return r
}

func validIdent(s string) bool {
	if s == "" || goReserved[s] {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// causeUnnamed and causeCodec join the [Gap] vocabulary. One string per reason,
// so counting them ranks what to fix.
const (
	causeUnnamed = "no name" // an op whose id spells no Go identifier
	// causeCodec is a type whose restatement here does not have the layout the
	// declared type has — a different field count, or a field in a different
	// place. The codec states the DECLARED offsets, so a restatement that moved
	// would read every field after the move from the wrong place and fail
	// nothing. A gap is the honest answer; a guessed layout is not.
	causeCodec = "no codec"
	// causeNested is a field whose own type has no wire form. The type holding
	// it has none either: a struct is its fields, and one that cannot cross
	// cannot be quietly left out of a value the caller thinks it sent.
	causeNested = "nested"
	// causeEmbedded is an embedded field whose type this package cannot name.
	// Go requires a name there, so there is nothing to write that parses.
	causeEmbedded = "embedded"
)

var goReserved = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
	// The package's own declarations. A type named Client would otherwise
	// replace the thing every method hangs off.
	"Client": true, "Dial": true, "Open": true, "Conn": true, "Close": true,
}
