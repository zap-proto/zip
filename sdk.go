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
func (s *SDK) Ops() int { return strings.Count(string(s.Source), "\nfunc (c *Client) ") - clientMethods }

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
	g := &render{sdk: &SDK{Package: pkg}, named: map[reflect.Type]string{}, taken: map[string]bool{}}

	ops := append([]*registeredOp(nil), a.Registry()...)
	sort.Slice(ops, func(i, j int) bool { return opName(ops[i]) < opName(ops[j]) })
	for _, op := range ops {
		g.method(op)
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
	taken map[string]bool         // every name claimed at package scope
	calls []call
	decls []string
}

// method resolves one operation. An op whose In or Out cannot cross this wire
// gets no method: a client that could be called and could never succeed is
// worse than one that says the operation is not reachable.
func (g *render) method(op *registeredOp) {
	id := opName(op)
	doc, hasDoc := docFor(op.Method, op.Path)
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
		return name, name != ""
	}
	if _, err := zapenc.LayoutOf(t); err != nil {
		g.named[t] = ""
		g.gap(op, goName(t), t.String(), causeOf(err))
		return "", false
	}

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
		goType := g.typeOf(f.Type, op, t.Name()+"."+f.Name, fields)
		if f.Anonymous {
			// An embedded field IS its type's name, and the wire cares: it takes
			// one nested slot, where the flattened form would take several.
			fmt.Fprintf(&b, "\t%s %s\n", goType, tagOf(f))
			continue
		}
		fmt.Fprintf(&b, "\t%s %s %s\n", f.Name, goType, tagOf(f))
	}
	b.WriteString("}\n")
	g.decls = append(g.decls, b.String())
	return name, true
}

// typeOf spells one field's Go type in the generated package: the same type it
// has here, with any struct it reaches renamed to the declaration this package
// makes for it.
func (g *render) typeOf(t reflect.Type, op, at string, fields map[string]string) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + g.typeOf(t.Elem(), op, at, fields)
	case reflect.Slice:
		return "[]" + g.typeOf(t.Elem(), op, at, fields)
	case reflect.Array:
		return "[" + strconv.Itoa(t.Len()) + "]" + g.typeOf(t.Elem(), op, at, fields)
	case reflect.Map:
		// A map has no layout: a key set is not a set of offsets. LayoutOf has
		// already refused the struct that holds it, so this only renders where
		// the map sits behind something the wire never reaches.
		return "map[" + g.typeOf(t.Key(), op, at, fields) + "]" + g.typeOf(t.Elem(), op, at, fields)
	case reflect.Struct:
		if t.NumField() == 0 {
			return "struct{}"
		}
		name, ok := g.declare(t, op, fields)
		if !ok {
			return "struct{}"
		}
		return name
	case reflect.Interface:
		g.gap(op, at, t.String(), CauseAny)
		return "any"
	}
	// A basic kind keeps its own spelling — including a named one's underlying
	// type, because the name belongs to the service's package and the width is
	// what the wire reads.
	return t.Kind().String()
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
	b.WriteString("import (\n\t\"context\"\n\n\t\"github.com/zap-proto/zip\"\n)\n\n")

	for _, d := range g.decls {
		b.WriteString(d)
		b.WriteString("\n")
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

// causeUnnamed joins the [Gap] vocabulary: an op whose id spells no Go
// identifier cannot have a method at all.
const causeUnnamed = "no name"

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
