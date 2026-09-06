package zip

// Reading a ZAP schema — the projection run backwards.
//
// [ZAPSchema] turns an app's typed ops into a .zap file. This turns a .zap file
// into an app, so that everything computed from a registry — the SDKs, the MCP
// tool list, the CLI, the documentation — can be computed for a service whose
// contract is a schema rather than a Go declaration. Nothing downstream changes:
// the ops that arrive here are the same [registeredOp] values [Get] and friends
// produce, and every projection reads them without knowing where they came from.
//
// # The parse is not here
//
// [github.com/zap-proto/go/idl] reads the text. It is the same package cmd/zapgen
// reads it with, which is the point: a second parser would be a second grammar,
// and the two would agree until they did not. What this file decides is only
// what the AST does not say — which Go type each declared type is, and where an
// op sits.
//
// # An op needs an address, and a schema has none
//
// A ZAP method is a name and one struct in each direction. A [registeredOp] is
// addressed, because every projection keys on the address. So a method becomes
// POST at its own name: uniform, and never a guess about which methods are safe
// to repeat — a schema states nothing about that, and reading `health()` as a GET
// would be this file inventing a semantic the author did not write. The op's id
// is the method's own name rather than one derived from the address, so a schema
// read here and projected back by [ZAPSchema] names the same methods.
//
// # The refusals
//
// The layout is the contract. A .zap field states an offset, and [LayoutOf]
// derives one; where the two disagree, an SDK generated from this schema would
// encode at offsets the schema does not declare. That is the one failure a
// schema exists to prevent, so it is refused, by field, with both numbers — and
// every such field is named, not just the first, because a work list that stops
// at the first understates it.
//
// The disagreement is not hypothetical and not rare. zip aligns each slot to its
// own width and gives a nested value eight bytes; the IDL's own offset assignment
// packs, and gives a nested value four. So a schema zip WROTE reads back exactly,
// and a schema whose offsets were packed elsewhere is refused rather than
// silently re-laid-out.

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/go/idl"
)

// ReadZAP is the apps a .zap schema declares: one per interface, each carrying
// one op per method and the structs that interface reaches.
//
// It is the exact inverse of [ZAPSchema], which takes any number of apps and
// writes them into one file as one interface each. So a file read here and
// projected back names the same interfaces, with the same methods, over the same
// structs — the ops are addressed and the methods are not, and that is the whole
// of what this adds.
//
// name is the schema's own name, used in errors and nowhere else. Each app is
// named for its interface.
//
// The apps have no handlers. A schema is a contract, and the implementation of
// that contract is not in this process — so the ops describe themselves fully to
// every projection, and refuse to run.
func ReadZAP(name string, src []byte) ([]*App, error) {
	file, err := idl.Parse(name, src)
	if err != nil {
		return nil, err
	}

	// A schema with no interface declares data, not a service. It is a perfectly
	// good schema and there is nothing here to project: every projection is a
	// projection of operations, and this file has none. Saying so is better than
	// answering with an app whose registry is empty, which is the failure this
	// whole path exists to end.
	if len(file.Interfaces) == 0 {
		return nil, fmt.Errorf("%s: declares %d struct(s) and no interface, so it declares no operations", name, len(file.Structs))
	}

	r := &reader{
		name:  name,
		decl:  make(map[string]*idl.Struct, len(file.Structs)),
		built: map[string]reflect.Type{},
		doing: map[string]bool{},
	}
	for _, s := range file.Structs {
		if _, dup := r.decl[s.Name]; dup {
			return nil, fmt.Errorf("%s: struct %s is declared twice", name, s.Name)
		}
		r.decl[s.Name] = s
	}

	apps := make([]*App, 0, len(file.Interfaces))
	for _, iface := range file.Interfaces {
		app := New(Config{AppName: iface.Name})
		seen := map[string]bool{}
		for _, m := range iface.Methods {
			if seen[m.Name] {
				// Two methods of one name are two ops under one id, and every
				// projection keys on the id: the second would take the first's
				// place in the SDK, the tool list and the document.
				r.refuse("%s.%s is declared twice", iface.Name, m.Name)
				continue
			}
			seen[m.Name] = true
			in, inOK := r.payload(iface.Name+"."+m.Name, "request", m.Request)
			out, outOK := r.payload(iface.Name+"."+m.Name, "reply", m.Response)
			if !inOK || !outOK {
				continue
			}
			app.schemaOp(m.Name, in, out)
		}
		apps = append(apps, app)
	}
	if err := r.err(); err != nil {
		return nil, err
	}
	return apps, nil
}

// reader turns one schema's declarations into Go types, and collects every
// reason it could not.
//
// It collects rather than returns at the first, because the answer a reader
// wants is the whole work list. A refusal naming one field of one struct, when
// four fields of three structs disagree, is three more rounds of the same
// conversation.
type reader struct {
	name  string
	decl  map[string]*idl.Struct
	built map[string]reflect.Type
	doing map[string]bool // the declarations being built, so a cycle is caught
	bad   []string
}

func (r *reader) refuse(format string, args ...any) {
	r.bad = append(r.bad, fmt.Sprintf(format, args...))
}

func (r *reader) err() error {
	if len(r.bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s: cannot be read as an app:\n  %s", r.name, strings.Join(r.bad, "\n  "))
}

// void is the type of a direction that carries nothing. It is a struct with no
// fields rather than a nil type because that is what a Go op with no input is —
// zip.Post[struct{}, Out] — and every projection already reads a fieldless
// struct as an absent payload.
var void = reflect.TypeFor[struct{}]()

// payload is the Go type one direction of a method carries. The bool is whether
// it could be read at all.
func (r *reader) payload(method, dir string, p *idl.Param) (reflect.Type, bool) {
	if p == nil {
		return void, true
	}
	t, err := r.structOf(p.StructName)
	if err != nil {
		r.refuse("%s %s: %v", method, dir, err)
		return nil, false
	}
	return t, true
}

// structOf is the Go type for one declared struct, built once.
func (r *reader) structOf(name string) (reflect.Type, error) {
	if t, ok := r.built[name]; ok {
		return t, nil
	}
	if r.doing[name] {
		// A field IS an offset and a width, so a declaration that reaches itself
		// has no fixed size to state. LayoutOf refuses the same thing one layer
		// down; catching it here is what keeps reflect.StructOf from recursing
		// until the stack ends.
		return nil, fmt.Errorf("struct %s contains itself, so it has no layout", name)
	}
	s, ok := r.decl[name]
	if !ok {
		return nil, fmt.Errorf("struct %s is not declared in this schema", name)
	}
	r.doing[name] = true
	defer delete(r.doing, name)

	fields := make([]reflect.StructField, 0, len(s.Fields))
	taken := map[string]string{} // Go field name → the schema field that claimed it
	for _, f := range s.Fields {
		ft, err := r.typeOf(f.Type)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", name, f.Name, err)
		}
		// The Go field must be exported to be read by reflection at all, and the
		// schema's spelling is kept on the JSON edge, so a name the schema wrote
		// in lower case still crosses under the name it was written with.
		goName := exportIdent(f.Name)
		if prior, dup := taken[goName]; dup {
			return nil, fmt.Errorf("%s: fields %s and %s are both %s to Go", name, prior, f.Name, goName)
		}
		taken[goName] = f.Name
		fields = append(fields, reflect.StructField{
			Name: goName,
			Type: ft,
			// decl carries the name this struct was declared under. A type built
			// at run time has none of its own — reflect makes no named types —
			// and two structs that differ only in name would otherwise be one
			// type, so `struct Ping { Seq u64 @0 }` and `struct Pong { Seq u64
			// @0 }` would collapse and a method declared to answer Pong would
			// answer Ping. See [typeName].
			Tag: reflect.StructTag(fmt.Sprintf("json:%q decl:%q", f.Name, name)),
		})
	}
	t := reflect.StructOf(fields)

	shape, err := LayoutOf(t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	// The schema STATES an offset and LayoutOf DERIVES one. Where they differ the
	// schema describes a wire this process does not speak, and everything
	// generated from it — a codec, a peer's accessors, the schema written back
	// out — would sit at the wrong bytes. Every field is checked, so the answer
	// is the whole disagreement rather than its first line.
	var off []string
	for i, sl := range shape.Slots {
		if want := s.Fields[i].Offset; sl.Offset != want {
			off = append(off, fmt.Sprintf("%s.%s is declared at @%d and lays out at @%d",
				name, s.Fields[i].Name, want, sl.Offset))
		}
	}
	if len(off) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(off, "; "))
	}

	r.built[name] = t
	return t, nil
}

// typeOf is the Go type for one schema type.
//
// It decides the mapping and nothing else: whether the result can cross is
// [LayoutOf]'s answer, asked once above on the whole struct. A list of lists and
// a list of a type with no wire form are refused there, in the words the encoder
// uses, rather than by a second opinion here that would eventually disagree
// with it.
func (r *reader) typeOf(t idl.Type) (reflect.Type, error) {
	switch t.Kind {
	case idl.KindBool:
		return reflect.TypeFor[bool](), nil
	case idl.KindU8:
		return reflect.TypeFor[uint8](), nil
	case idl.KindU16:
		return reflect.TypeFor[uint16](), nil
	case idl.KindU32:
		return reflect.TypeFor[uint32](), nil
	case idl.KindU64:
		return reflect.TypeFor[uint64](), nil
	case idl.KindI8:
		return reflect.TypeFor[int8](), nil
	case idl.KindI16:
		return reflect.TypeFor[int16](), nil
	case idl.KindI32:
		return reflect.TypeFor[int32](), nil
	case idl.KindI64:
		return reflect.TypeFor[int64](), nil
	case idl.KindF32:
		return reflect.TypeFor[float32](), nil
	case idl.KindF64:
		return reflect.TypeFor[float64](), nil
	case idl.KindText:
		return reflect.TypeFor[string](), nil
	case idl.KindBytes:
		return reflect.TypeFor[[]byte](), nil
	case idl.KindBytesFixed:
		return reflect.ArrayOf(t.FixedSize, reflect.TypeFor[byte]()), nil
	case idl.KindList:
		elem, err := r.typeOf(*t.ListElem)
		if err != nil {
			return nil, err
		}
		return reflect.SliceOf(elem), nil
	case idl.KindStruct:
		return r.structOf(t.StructName)
	}
	return nil, fmt.Errorf("type %s has no Go form", t.Kind)
}

// schemaOp registers one op whose contract is declared and whose implementation
// is elsewhere.
//
// It is [registerTyped] without the type parameters, because the types arrive as
// reflect.Types and Go has no way to make them type parameters. What it must
// therefore build by hand is exactly the bookkeeping every projection reads —
// the address, the id, and the two types — and what it does not build is the
// three seams that run a handler, because there is no handler: a schema states
// what an op takes and answers, never what it does.
func (a *App) schemaOp(name string, in, out reflect.Type) {
	const method = fiber.MethodPost
	path := "/" + name
	op := &registeredOp{
		Method:      method,
		Path:        path,
		OperationID: name,
		InType:      in,
		OutType:     out,
	}
	op.rule = a.rule

	// Every way into this op refuses the same way. A declared op that answered
	// anything would be answering for an implementation nobody wrote.
	unimplemented := func() error {
		return Errorf(501, "%s is declared by a schema; no implementation of it is linked into this process", name)
	}
	op.invoke = func(context.Context, decoder, []byte, map[string]string, map[string]string, func(string) string) (any, error) {
		return nil, unimplemented()
	}
	op.direct = func(context.Context, any) (any, error) { return nil, unimplemented() }

	a.addRoute(here(2), route{
		method: method,
		path:   path,
		serve:  func(fiber.Ctx) error { return unimplemented() },
		op:     op,
	})
}
