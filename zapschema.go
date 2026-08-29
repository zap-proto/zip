package zip

// ZAP schema — the SEVENTH projection, and the only one that can refuse.
//
// The same typed-op registry (a.Registry()) that produces the REST routes, the
// OpenAPI document, the MCP tool list, the CLI, the op-call plane and the
// GraphQL schema also produces a ZAP IDL file: In becomes the method's request
// struct, Out becomes its reply, and the ops of one app become one interface.
// Nothing new is declared, so a method exists because an op exists.
//
// # Why this one is different
//
// Every other projection can describe anything. JSON Schema has `{}`, GraphQL
// has a JSON scalar, the CLI has a string flag. Each of those is a way of
// writing down "I do not know what this is" and carrying on. ZAP has no such
// word: a field IS an offset and a width, so a value the IDL cannot name is a
// value that cannot cross. That is not a defect of the IDL, it is the property
// that makes a read pointer arithmetic instead of a parse.
//
// So this projection answers with TWO things — the schema, and the list of ops
// that are not in it and why (see [Gap]). A projection that emitted a struct
// with the awkward field quietly dropped would publish a contract the wire does
// not keep, which is the one failure a schema exists to prevent.
//
// # The layout is NOT derived here
//
// Every offset, width and type in the emitted text comes from [LayoutOf], the
// same derivation [Call] encodes against. That is the whole design: a schema
// that re-derived the layout would describe a wire nobody speaks, and the two
// would disagree first about exactly the things that are easy to get right by
// reasoning and wrong in fact — offsets are ALIGNED rather than packed, and a
// nested value takes EIGHT bytes rather than four.
//
// What this file decides is only what a layout cannot say: which ops can be
// expressed at all, what each declaration is NAMED, and why a refusal happened.

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// ZAPSchema renders the ZAP IDL that apps' typed ops describe, under pkg.
//
// The text is the artifact — it is what zapgen compiles into accessors, and
// publishing it is what lets anything that is not this process speak to it
// without a copy of these Go types. The [Schema.Gaps] are the other half of the
// answer and are not decoration: an op named there is NOT in the text.
//
// ONE INTERFACE PER APP, and one struct namespace across all of them. An
// interface is a declared service and its method ordinals are positional, so a
// fleet rendered as one interface would renumber every service's methods when
// any service gained one. Types are the opposite: an Application is an
// Application wherever it is reached, so it is declared once and referred to by
// name — the same split the JSON document already makes between an operationId,
// which belongs to the occurrence, and a schema, which belongs to the type.
func ZAPSchema(pkg string, apps ...*App) *Schema {
	s := &Schema{Package: pkg}
	e := &emitter{schema: s, named: map[reflect.Type]string{}, taken: map[string]bool{}}
	for i, a := range apps {
		ops := append([]*registeredOp(nil), a.Registry()...)
		sort.Slice(ops, func(i, j int) bool { return opName(ops[i]) < opName(ops[j]) })

		// The interface is named for the app it projects. A single app carries the
		// package's name — describing one app, the file and the service it
		// declares are the same thing, and "app0" is an ordinal standing where a
		// name belongs. The ordinal survives only where it is the truth: several
		// unnamed apps in one file, which is a fleet compose and not a subset.
		name := a.cfg.AppName
		if name == "" {
			if len(apps) == 1 {
				name = pkg
			} else {
				name = "app" + strconv.Itoa(i)
			}
		}
		iface := &Interface{Name: idlName(name)}
		e.iface = iface

		// An op that CAN spell its own name keeps it. Claiming those first is what
		// stops a folded neighbour taking a name its owner could have had: sorted,
		// '-' precedes '_', so get_a-b would otherwise claim get_a_b and the op
		// actually called get_a_b would be ordinalled — a published name becoming a
		// function of who else is in the room, which is the failure the JSON schema
		// registry already refuses one layer up.
		e.methods = map[string]bool{}
		for _, op := range ops {
			if id := opName(op); idlName(id) == id {
				e.methods[id] = true
			}
		}
		for _, op := range ops {
			e.method(op)
		}
		if len(iface.Methods) > 0 {
			s.Interfaces = append(s.Interfaces, iface)
		}
	}
	sort.Slice(s.Structs, func(i, j int) bool { return s.Structs[i].Name < s.Structs[j].Name })
	sort.Slice(s.Interfaces, func(i, j int) bool { return s.Interfaces[i].Name < s.Interfaces[j].Name })
	sort.Slice(s.Gaps, func(i, j int) bool {
		if s.Gaps[i].Op != s.Gaps[j].Op {
			return s.Gaps[i].Op < s.Gaps[j].Op
		}
		return s.Gaps[i].Field < s.Gaps[j].Field
	})
	sort.Slice(s.Opaque, func(i, j int) bool {
		if s.Opaque[i].Struct != s.Opaque[j].Struct {
			return s.Opaque[i].Struct < s.Opaque[j].Struct
		}
		return s.Opaque[i].Field < s.Opaque[j].Field
	})
	sort.Slice(s.Dropped, func(i, j int) bool {
		if s.Dropped[i].Struct != s.Dropped[j].Struct {
			return s.Dropped[i].Struct < s.Dropped[j].Struct
		}
		return s.Dropped[i].Field < s.Dropped[j].Field
	})
	sort.Slice(s.Coded, func(i, j int) bool {
		if s.Coded[i].Struct != s.Coded[j].Struct {
			return s.Coded[i].Struct < s.Coded[j].Struct
		}
		return s.Coded[i].Field < s.Coded[j].Field
	})
	return s
}

// Schema is one .zap file: the structs the ops reach, the interfaces they form,
// and the ops that could not be expressed at all.
type Schema struct {
	Package    string
	Structs    []*Struct
	Interfaces []*Interface
	Gaps       []Gap
	// Renamed is every op whose id the IDL's lexer cannot spell. Not a gap — the
	// op IS in the schema — but a divergence between the name it carries here and
	// the one it carries on every other surface, which is worth saying out loud.
	Renamed []Rename
	// Opaque is every field that crosses correctly and arrives WITHOUT ITS NAME:
	// a nested value, which this wire carries as a complete message inside an
	// {offset,length} slot. See [Opacity].
	Opaque []Opacity
	// Dropped is every field whose VALUE does not cross at all, on a type the
	// layout accepts. See [Loss] — it is the most expensive list here, because
	// nothing fails.
	Dropped []Loss
	// Coded is every field that this schema states correctly and the REFLECTIVE
	// encoder will not carry. See [Coded].
	Coded []Coded
}

// Coded is one field the schema states and reflection refuses: bytes_fixed[N].
//
// [LayoutOf] gives it an offset and a width, so the declaration here is right
// and a generated codec reads and writes it inline. What refuses is the
// reflective path, deliberately — an id is exactly the case that should force a
// type to declare its own wire (see [Wire]) rather than acquire one silently.
//
// So this is not a gap and not a loss. It is the list of ops that need their
// codec GENERATED before they can cross, which is the direction the whole
// migration goes anyway; these are simply the ones that cannot wait.
type Coded struct {
	Struct string
	Field  string
	Type   string // bytes_fixed[N], as the schema states it
}

// Interface is one app's declared service: its ops, as methods, in name order.
type Interface struct {
	Name    string
	Methods []*Method
}

// Ops is how many ops the schema expresses, across every interface.
func (s *Schema) Ops() int {
	n := 0
	for _, i := range s.Interfaces {
		n += len(i.Methods)
	}
	return n
}

// Blocked is how many DISTINCT ops are absent from the schema. len(Gaps) counts
// reasons, and one op can have several.
func (s *Schema) Blocked() int {
	ops := map[string]bool{}
	for _, g := range s.Gaps {
		ops[g.Op] = true
	}
	return len(ops)
}

// Struct is one declared struct: its fields in slot order, and the byte size of
// its fixed area. Both come from [LayoutOf].
type Struct struct {
	Name   string
	Fields []Field
	Size   int
	// From is the Go type this was derived from. It is not in the emitted text —
	// the schema is the contract and the Go type is one implementation of it —
	// but it is what a reader of the gap list needs in order to find the
	// declaration to change.
	From string
}

// Field is one slot: an IDL type at a byte offset.
type Field struct {
	Name   string
	Type   string
	Offset int
}

// Method is one op: its name from [ID], and the structs it takes and returns.
// An empty Request or Reply is an op with no input or no answer, which the IDL
// writes as an empty parameter list rather than as a struct with no fields.
type Method struct {
	Name    string
	Request string
	Reply   string

	// Doc is the handler's own doc comment, the same sentence the OpenAPI
	// document and the MCP tool description carry. It is read from the doc
	// registry rather than restated, so a schema and a document that disagree
	// about what a method does cannot happen: there is one sentence.
	Doc string
}

// Rename is one op whose method name had to differ from its id.
type Rename struct {
	Op     string // the id [ID] gives it, and every other surface uses
	Method string // what this schema had to call it
}

// Opacity is one field whose TYPE the schema cannot name, though its bytes cross
// exactly.
//
// A nested value is carried as a complete ZAP message inside an 8-byte
// {offset,length} slot — `bytes` — where the IDL's own nested struct is a 4-byte
// relative offset. Those are different slots, so naming the type here would
// state a width the encoder does not write and move every field after it. The
// bytes are therefore right and the name is gone: a peer reading this schema
// learns that something crosses without learning what.
//
// It is not a [Gap]: the op IS in the schema and its wire is exact. It is the
// measure of how much of the shape the schema can carry today, and it closes the
// day a nested value crosses as the IDL's own struct.
type Opacity struct {
	Struct string // the declaration the field sits in
	Field  string
	Go     string // the Go type whose name is lost
	List   bool   // it is the ELEMENT of a list that is opaque
}

// Loss is one field that is part of the declaration and carries NOTHING.
//
// It is worse than a [Gap] and worse than an [Opacity], because neither the
// layout nor the encoder nor the schema reports anything: the field is present,
// the message is well formed, and the value is simply not in it. A caller reads
// a zero and cannot tell it from a zero that was sent.
type Loss struct {
	Struct string // the declaration the field sits in
	Field  string
	Go     string // the Go type whose value is dropped
	Cause  string // LossEmpty or LossPromoted
}

// The two ways a value is dropped, both measured rather than imagined.
const (
	// LossEmpty is a nested value whose own layout has no slots — a type whose
	// fields are all unexported. It crosses as a complete, EMPTY message: eight
	// bytes of {offset,length} pointing at nothing. time.Time is the common one.
	LossEmpty = "empty message"
	// LossPromoted is an EMBEDDED type whose field name is therefore lowercase,
	// so the layout skips it as unexported while encoding/json PROMOTES its
	// fields to the outer object. The same value crosses over JSON and does not
	// cross here, which is the shape of divergence a reader of both will not
	// think to check.
	LossPromoted = "promoted, not carried"
)

// Gap is one op the IDL cannot express, and the reason.
//
// This is the cost of going ZAP-native stated as a number rather than as a
// worry. Cause groups them: it is the same string for every field that fails
// the same way, so counting by cause says which single change buys the most.
type Gap struct {
	Op    string // the op's name, the one [ID] gives it
	Field string // Type.field — where the value that cannot cross sits
	Go    string // the Go type there
	Cause string // why it cannot cross, from the fixed vocabulary below
}

// The causes. One string per reason, so `sort | uniq -c` over them is a ranked
// list of what to fix and nothing has to be inferred from prose.
const (
	CauseMap       = "map"          // no map in the IDL: a key set is not a layout
	CauseAny       = "any"          // an interface field names no type at all
	CauseEmpty     = "no fields"    // nothing exported crosses, so there is no struct to declare
	CauseUnwirable = "no wire form" // chan, func, complex, a wide array, a slice of slices
	CauseReaches   = "reaches one"  // every field is fine; one of them holds a type that is not
)

// ---- emitting ---------------------------------------------------------------

type emitter struct {
	schema *Schema
	named  map[reflect.Type]string // type → the name it is declared under, "" for refused
	taken  map[string]bool         // every struct name already declared

	// iface is the app being described; its methods land there. methods is that
	// interface's own name set — a method name is unique within its service and a
	// struct name across the whole file, so the two cannot share one set.
	iface   *Interface
	methods map[string]bool

	// op is the op being described, so a gap can name it. A type reached by ten
	// ops is declared once, under the first op that reaches it, and a gap inside
	// it is reported against every op that reaches it — because every one of
	// those ops is what actually cannot cross.
	op string
}

// method describes one op. An op whose In or Out cannot be expressed contributes
// gaps and NO method: a method whose payload is a struct that was never declared
// is not a schema, it is a dangling reference.
func (e *emitter) method(op *registeredOp) {
	e.op = opName(op)
	req, reqOK := e.payload(op.InType)
	rep, repOK := e.payload(op.OutType)
	if !reqOK || !repOK {
		return
	}
	// The prose comes from the doc registry cmd/zipdoc fills from the handler's
	// own comment — the same Description the OpenAPI operation and the MCP tool
	// carry. A method's summary and its schema are then one sentence written
	// once, and cannot drift apart into two.
	var doc string
	if d, ok := docFor(op.Method, op.Path); ok {
		doc = d.Description
	}
	e.iface.Methods = append(e.iface.Methods, &Method{
		Name: e.methodName(e.op), Request: req, Reply: rep, Doc: doc,
	})
}

// methodName is the op's own id, spelled the way the IDL's lexer accepts.
//
// [ID] PRESERVES '-' and '.' because both are legal in an operationId and both
// tell two addresses apart — get_pricing-policy is not get_pricing_policy. The
// IDL's identifier is narrower: a letter or '_' first, then letters, digits and
// '_'. So a handful of ops cannot be spelled here under the name they carry
// everywhere else, and each one is RECORDED (see [Schema.Renamed]) rather than
// quietly folded — a name that differs on one surface out of seven is exactly
// what a reader of the other six will not think to check.
func (e *emitter) methodName(id string) string {
	if name := idlName(id); name == id {
		return id // already claimed above; the op spells its own name
	}
	name := idlName(id)
	for n := 2; e.methods[name]; n++ {
		name = idlName(id) + strconv.Itoa(n)
	}
	e.methods[name] = true
	e.schema.Renamed = append(e.schema.Renamed, Rename{Op: id, Method: name})
	return name
}

// payload names the struct one direction of a method carries, or "" for a
// direction that carries nothing. The bool is whether it could be expressed at
// all — "" with ok is a void direction, "" without is a refusal.
func (e *emitter) payload(t reflect.Type) (string, bool) {
	t = deref(t)
	if t == nil || t.Kind() != reflect.Struct {
		return "", true // no input, or no answer: an empty parameter list
	}
	if len(exported(t)) == 0 {
		return "", true // a marker struct with nothing in it is the same absence
	}
	name := e.define(t)
	if name == "" {
		return "", false
	}
	return name, true
}

// define declares t and returns its name, or "" when it cannot be declared.
//
// The LAYOUT is [LayoutOf]'s and this function computes none. What it adds is
// the name, and — when the layout refuses — the diagnosis, because LayoutOf
// stops at the first field that cannot cross and a work list has to name them
// all.
func (e *emitter) define(t reflect.Type) string {
	if name, ok := e.named[t]; ok {
		if name == "" {
			e.gap(t.Name(), goName(t), CauseReaches) // re-blame for THIS op
		}
		return name
	}

	shape, err := LayoutOf(t)
	if err != nil {
		e.named[t] = ""
		e.diagnose(t)
		return ""
	}
	if len(shape.Slots) == 0 {
		e.named[t] = ""
		e.gap(t.Name(), goName(t), CauseEmpty)
		return ""
	}

	name := e.name(t)
	e.named[t] = name
	e.taken[name] = true

	fields := make([]Field, 0, len(shape.Slots))
	for _, s := range shape.Slots {
		fields = append(fields, Field{Name: idlName(s.Name), Type: s.Type, Offset: s.Offset})
		e.opacity(t, name, s)
		if strings.HasPrefix(s.Type, "bytes_fixed[") || strings.HasPrefix(s.Elem, "bytes_fixed[") {
			e.schema.Coded = append(e.schema.Coded, Coded{Struct: name, Field: s.Name, Type: s.Type})
		}
	}
	e.dropped(t, name)
	e.schema.Structs = append(e.schema.Structs, &Struct{
		Name: name, Fields: fields, Size: shape.Size, From: goName(t),
	})
	return name
}

// opacity records a field whose bytes are exact and whose TYPE NAME is lost. A
// nested value crosses as `bytes`, so the schema says something is there and not
// what it is. See [Opacity].
func (e *emitter) opacity(t reflect.Type, decl string, s Slot) {
	f, ok := t.FieldByName(s.Name)
	if !ok {
		return
	}
	ft := deref(f.Type)
	if ft == nil {
		return
	}
	switch {
	case s.Type == "bytes" && ft.Kind() == reflect.Struct:
		e.schema.Opaque = append(e.schema.Opaque, Opacity{Struct: decl, Field: s.Name, Go: goName(ft)})
	case s.Elem == "bytes" && ft.Kind() == reflect.Slice:
		if et := deref(ft.Elem()); et != nil && et.Kind() == reflect.Struct {
			e.schema.Opaque = append(e.schema.Opaque, Opacity{Struct: decl, Field: s.Name, Go: goName(et), List: true})
		}
	}
}

// dropped names every field of t whose VALUE does not cross, on a type the
// layout accepted. Nothing fails here and nothing is reported by anything else,
// which is why it is worth walking the type a second time to find.
func (e *emitter) dropped(t reflect.Type, decl string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			// An unexported field crosses nowhere and is nobody's contract —
			// EXCEPT an embedded type, whose field name is its type name and so is
			// lowercase for an unexported type. encoding/json promotes its fields
			// to the outer object; the layout skips it. Same value, two wires.
			if f.Anonymous && len(exported(f.Type)) > 0 {
				e.schema.Dropped = append(e.schema.Dropped, Loss{
					Struct: decl, Field: f.Name, Go: goName(deref(f.Type)), Cause: LossPromoted,
				})
			}
			continue
		}
		if inner := hollow(f.Type); inner != nil {
			e.schema.Dropped = append(e.schema.Dropped, Loss{
				Struct: decl, Field: f.Name, Go: goName(inner), Cause: LossEmpty,
			})
		}
	}
}

// hollow reports the nested type a field carries as an EMPTY message, or nil.
// A struct whose fields are all unexported lays out with no slots, so it crosses
// as eight bytes pointing at a message with nothing in it.
func hollow(t reflect.Type) reflect.Type {
	t = deref(t)
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Slice {
		t = deref(t.Elem())
		if t == nil {
			return nil
		}
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	if shape, err := LayoutOf(t); err == nil && len(shape.Slots) == 0 {
		return t
	}
	return nil
}

// diagnose names EVERY field of t that cannot cross, and why.
//
// It exists because [LayoutOf] answers the question the ENCODER asks — can this
// type cross, yes or no — and stops at the first field that cannot. A work list
// has to name them all: a list that stopped at the first understates the
// migration by however many problems each type has.
//
// It decides NOTHING about the wire. It walks the same fields the layout walks,
// and for the verdict it asks the layout itself (see [crosses]) rather than
// holding a second opinion about what a slot may be — the causes below are
// narrower questions asked FIRST, purely so the answer is a reason and not a
// shrug.
func (e *emitter) diagnose(t reflect.Type) {
	for _, f := range exported(t) {
		if crosses(f.Type) {
			continue // this field is fine; another one is why the type refused
		}
		ft := deref(f.Type)
		path := t.Name() + "." + f.Name
		if ft == nil {
			e.gap(path, "nil", CauseAny)
			continue
		}
		switch ft.Kind() {
		case reflect.Map:
			e.gap(path, goName(ft), CauseMap)
		case reflect.Interface:
			e.gap(path, goName(ft), CauseAny)
		case reflect.Struct:
			e.gap(path, goName(ft), CauseReaches)
		default:
			e.gap(path, goName(ft), CauseUnwirable)
		}
	}
}

// crosses asks the LAYOUT whether one field's type can be a slot, by laying out
// a struct holding exactly that field. It is the layout's answer and not a
// second classification — which is the point: a diagnosis with its own opinion
// about what crosses would eventually contradict the encoder, and a work list
// that disagrees with the wire sends people to fix things that are not broken.
func crosses(t reflect.Type) bool {
	probe := reflect.StructOf([]reflect.StructField{{Name: "X", Type: t}})
	_, err := LayoutOf(probe)
	return err == nil
}

// exported is the fields a layout takes: the type's own, exported, in
// declaration order. An unexported field cannot be read or written by
// reflection, so it never takes a slot.
//
// It is NOT wireFields. That rule PROMOTES an embedded struct's fields the way
// encoding/json does, which is right for the JSON edge and is not what this wire
// carries: the layout gives an embedded struct ONE slot of its own. Describing
// the promoted shape would publish a struct whose fields sit at offsets nothing
// writes.
func exported(t reflect.Type) []reflect.StructField {
	t = deref(t)
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	var out []reflect.StructField
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); f.IsExported() {
			out = append(out, f)
		}
	}
	return out
}

func (e *emitter) gap(field, goType, cause string) {
	e.schema.Gaps = append(e.schema.Gaps, Gap{Op: e.op, Field: field, Go: goType, Cause: cause})
}

// ---- names ------------------------------------------------------------------

// name is the IDL name for t: its Go name, qualified by its package when a
// DIFFERENT type already holds that name, then by an ordinal if even that
// collides. It is the JSON registry's rule in the IDL's character set — one
// naming idea, not two — and it is deterministic because the ops are walked in
// sorted order, so the first claimant is the same on every run.
func (e *emitter) name(t reflect.Type) string {
	base := idlName(t.Name())
	if base == "" || base == "_" {
		base = idlName(e.op) + "_anon"
	}
	if !e.taken[base] {
		return base
	}
	if p := t.PkgPath(); p != "" {
		base = idlName(p[strings.LastIndexByte(p, '/')+1:]) + "_" + base
	}
	for name, n := base, 2; ; n++ {
		if !e.taken[name] {
			return name
		}
		name = base + strconv.Itoa(n)
	}
}

// idlName reduces s to a name the IDL's lexer accepts AND its parser will not
// read as something else: a letter or '_' first, then letters, digits and '_',
// and never one of the words the grammar already spends.
//
// The keyword guard is not decoration. A struct called `text` parses as the
// scalar wherever it is referred to, so the field would silently change type; a
// struct called `list` would not parse at all.
func idlName(s string) string {
	out := ident(s)
	if reserved[out] {
		return out + "_"
	}
	return out
}

// reserved is every word the grammar already spends: the scalars, the two type
// constructors, and the top-level and method keywords.
var reserved = map[string]bool{
	"bool": true, "u8": true, "u16": true, "u32": true, "u64": true,
	"i8": true, "i16": true, "i32": true, "i64": true, "f32": true, "f64": true,
	"text": true, "bytes": true, "bytes_fixed": true, "list": true,
	"package": true, "struct": true, "interface": true, "type": true, "returns": true,
}

// ident reduces s to what the IDL's lexer accepts for a name: a letter or '_'
// first, then letters, digits and '_'.
func ident(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9' && i > 0:
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "_"
	}
	if c := out[0]; c >= '0' && c <= '9' {
		return "_" + out
	}
	return out
}

// goName is the Go type as a person would grep for it. It is for the gap list,
// which is read by someone about to go and change that declaration.
func goName(t reflect.Type) string {
	if t == nil {
		return "nil"
	}
	if p := t.PkgPath(); p != "" && t.Name() != "" {
		return p[strings.LastIndexByte(p, '/')+1:] + "." + t.Name()
	}
	return t.String()
}

// ---- rendering --------------------------------------------------------------

// String is the .zap file: package, structs, interfaces.
//
// Sorted throughout — structs by name, interfaces by app name, methods by op
// name — so the same program renders the same bytes on every run and a diff
// shows a change to the CONTRACT rather than a change to map iteration.
// docLines splits a doc comment into comment lines, dropping blanks so a
// paragraph break does not become a bare "#" in the schema.
func docLines(doc string) []string {
	if doc == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (s *Schema) String() string {
	var b strings.Builder
	b.WriteString("# Generated from typed ops. Do not edit.\n")
	b.WriteString("# Every struct and method below is derived from one op's In and Out, and\n")
	b.WriteString("# every offset from the layout the op-call plane encodes against.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", s.Package)

	for _, st := range s.Structs {
		fmt.Fprintf(&b, "struct %s {\n", st.Name)
		nw, tw := 0, 0
		for _, f := range st.Fields {
			nw, tw = max(nw, len(f.Name)), max(tw, len(f.Type))
		}
		for _, f := range st.Fields {
			fmt.Fprintf(&b, "    %-*s %-*s @%d\n", nw, f.Name, tw, f.Type, f.Offset)
		}
		b.WriteString("}\n\n")
	}

	for _, in := range s.Interfaces {
		fmt.Fprintf(&b, "interface %s {\n", in.Name)
		for _, m := range in.Methods {
			for _, line := range docLines(m.Doc) {
				b.WriteString("    # " + line + "\n")
			}
			b.WriteString("    " + m.Name + "(")
			if m.Request != "" {
				b.WriteString("req: " + m.Request)
			}
			b.WriteString(")")
			if m.Reply != "" {
				b.WriteString(" returns (rep: " + m.Reply + ")")
			}
			b.WriteString("\n")
		}
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
