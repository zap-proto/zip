package zip

// The Go codec — the source that STATES a type's ZAP wire instead of having one
// derived from it at every call.
//
// [LayoutOf] names three readers of the one derivation: the plane encodes
// against it, a .zap schema states it as `Name type @Offset`, and a generator
// emits it as constants. This is that generator. What it writes is a [Wire]
// implementation: two methods per type, offsets as constants, no reflection on
// the served path.
//
// # Why generate rather than reflect
//
// The derivation is cached; the COPY is not. Every reflective call walks the
// fields again and sizes a builder by guess — work whose answer was fixed the
// moment the type was declared.
//
// It is also the only way to carry an id. bytes_fixed[N] has an offset and a
// width, so the layout knows its shape and a schema states it correctly, and the
// reflective encoder refuses it outright — deliberately, because an id is
// exactly the case that should force a type to declare its own wire. An ids.ID
// is [32]byte, so today a reply carrying one cannot cross the plane at all.
//
// # The wire does not move
//
// The emitted code writes the SAME BYTES the reflective encoder writes, field
// for field and in the same order, for every field the reflective encoder can
// write at all. That is not a promise about intent — it falls out of reading the
// offsets from [LayoutOf] rather than deriving them a second time, and
// TestTheCodecKeepsTheWire holds it against the reflective encoder directly.
//
// The sequence it reproduces: every list is written FIRST, because SetList takes
// an offset that must already exist; then the object is started at the declared
// size; then each field is set in declaration order; then the list slots are
// filled. Deferred text and bytes are laid down by the builder in the order they
// were set, so declaration order is load-bearing and not cosmetic.
//
// # What the emitted package depends on
//
// The ZAP builder, and nothing else. [Wire] is restated in the emitted file, the
// way internal/zapenc restates it, so a leaf module — an id package, a shared
// types package — can state its wire without taking on this one's dependencies.
// The restatement is a compile-time assertion per type, which is what makes a
// deleted codec a build failure rather than a silent return to reflection.

import (
	"bytes"
	"fmt"
	"go/format"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Codec is one emitted file: the Go source stating the wire of every type in one
// package, and the names of the types it states.
type Codec struct {
	Path    string   // the package's import path
	Package string   // its package clause
	Types   []string // the types declared, alphabetically
	Source  []byte
}

// Codecs renders a [Wire] implementation for each of roots and for every struct
// nested below them, grouped by the package that owns the type.
//
// A nested value is reached BY ITS METHOD, so the set is closed downward: a
// parent that states its wire needs every struct it holds to state one too, id
// or no id. The exception is a value with no slots — see [empty] — which crosses
// as bytes the parent writes inline.
//
// A type the derivation refuses — one holding a map, an interface, or a fixed
// array of anything but bytes — comes back as an error naming it, because there
// is no layout to state and a codec that guessed one would speak a wire nobody
// reads. Answering that is a change to the TYPE, which is not a generator's to
// make.
func Codecs(roots ...reflect.Type) ([]Codec, error) {
	want := map[reflect.Type]bool{}
	var refused []string
	for _, r := range roots {
		if err := reach(r, want); err != nil {
			refused = append(refused, err.Error())
		}
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return nil, fmt.Errorf("zip: %s", strings.Join(dedup(refused), "; "))
	}

	byPath := map[string][]reflect.Type{}
	for t := range want {
		byPath[t.PkgPath()] = append(byPath[t.PkgPath()], t)
	}
	out := make([]Codec, 0, len(byPath))
	for path, ts := range byPath {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Name() < ts[j].Name() })
		c, err := one(path, ts, want)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// reach adds t and everything below it to want.
func reach(t reflect.Type, want map[reflect.Type]bool) error {
	t = under(t)
	if t == nil || t.Kind() != reflect.Struct || want[t] {
		return nil
	}
	shape, err := LayoutOf(t)
	if err != nil {
		return fmt.Errorf("%s has no layout: %w", goname(t), err)
	}
	want[t] = true
	for _, s := range shape.Slots {
		f, ok := held(t, s.Name)
		if !ok {
			continue
		}
		if n := nests(f.Type); n != nil && !empty(n) {
			if err := reach(n, want); err != nil {
				return err
			}
		}
	}
	return nil
}

// empty reports a value with no slots — every field unexported, which is what a
// time.Time or a netip.AddrPort is from here. It crosses as a complete and EMPTY
// ZAP object and carries nothing, so a parent writes those bytes inline rather
// than reaching for a method, and there is nothing to declare a wire for. The
// value is lost either way; that is the reflective encoder's answer too, and
// changing it would be a different wire.
func empty(t reflect.Type) bool {
	sh, err := LayoutOf(t)
	return err == nil && len(sh.Slots) == 0
}

// nests is the struct a field reaches through a pointer or a list, or nil. A
// slot is "bytes" for both a []byte and a nested value, so the Go type is what
// tells them apart.
func nests(t reflect.Type) reflect.Type {
	t = under(t)
	if t.Kind() == reflect.Slice {
		t = under(t.Elem())
	}
	if t.Kind() == reflect.Struct {
		return t
	}
	return nil
}

func under(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// held is the DIRECT field of that name. FieldByName would also find one
// promoted through an embedded struct, and an embedded struct is a slot of its
// own here — its fields are nested, never flattened.
func held(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		if f := t.Field(i); f.Name == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func goname(t reflect.Type) string {
	if t.PkgPath() == "" {
		return t.String()
	}
	return t.PkgPath() + "." + t.Name()
}

func dedup(s []string) []string {
	out := s[:0]
	var last string
	for _, v := range s {
		if v != last {
			out = append(out, v)
		}
		last = v
	}
	return out
}

// ---- emission --------------------------------------------------------------

// pkg is the import set of one emitted file: an alias per path, so a name that
// two packages share is unambiguous in the source rather than in a convention.
// wide records whether any list of scalars was emitted, which is the one helper
// the file may need.
type pkg struct {
	self  string
	alias map[string]string
	taken map[string]bool
	plain map[string]bool // imports whose alias is their own name
	wide  bool
	blank bool
}

func (p *pkg) of(path, name string) string {
	if path == "" || path == p.self {
		return ""
	}
	if a, ok := p.alias[path]; ok {
		return a
	}
	a := name
	for n := 2; p.taken[a]; n++ {
		a = name + strconv.Itoa(n)
	}
	p.taken[a] = true
	p.alias[path] = a
	if a == name {
		p.plain[path] = true
	}
	return a
}

// std takes a standard-library import the emitted code needs directly.
func (p *pkg) std(path string) { p.of(path, path[strings.LastIndex(path, "/")+1:]) }

// spell renders t as Go source inside this file, taking the import it needs.
func (p *pkg) spell(t reflect.Type) string {
	if t.Name() != "" {
		if a := p.of(t.PkgPath(), strings.SplitN(t.String(), ".", 2)[0]); a != "" {
			return a + "." + t.Name()
		}
		return t.Name()
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + p.spell(t.Elem())
	case reflect.Slice:
		return "[]" + p.spell(t.Elem())
	case reflect.Array:
		return "[" + strconv.Itoa(t.Len()) + "]" + p.spell(t.Elem())
	}
	return t.String()
}

func one(path string, ts []reflect.Type, want map[reflect.Type]bool) (Codec, error) {
	p := &pkg{
		self:  path,
		alias: map[string]string{},
		taken: map[string]bool{"zap": true, "fmt": true},
		plain: map[string]bool{},
	}
	var body bytes.Buffer
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		if err := declare(&body, t, p); err != nil {
			return Codec{}, err
		}
		names = append(names, t.Name())
	}

	clause := path[strings.LastIndex(path, "/")+1:]
	if len(ts) > 0 {
		clause = strings.SplitN(ts[0].String(), ".", 2)[0]
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "// Code generated by zip; DO NOT EDIT.\n\npackage %s\n\nimport (\n\t\"fmt\"\n", clause)
	paths := make([]string, 0, len(p.alias))
	for q := range p.alias {
		paths = append(paths, q)
	}
	sort.Strings(paths)
	for _, q := range paths {
		if p.plain[q] && !strings.Contains(q, ".") {
			fmt.Fprintf(&out, "\t%q\n", q)
		}
	}
	fmt.Fprint(&out, "\n\tzap \"github.com/zap-proto/go\"\n")
	for _, q := range paths {
		if !p.plain[q] || strings.Contains(q, ".") {
			fmt.Fprintf(&out, "\t%s %q\n", p.alias[q], q)
		}
	}
	fmt.Fprint(&out, ")\n\n", preamble)
	if p.wide {
		fmt.Fprint(&out, widening)
	}
	if p.blank {
		fmt.Fprint(&out, emptying)
	}
	out.Write(body.Bytes())

	src, err := format.Source(out.Bytes())
	if err != nil {
		return Codec{}, fmt.Errorf("zip: emitted source for %s does not parse: %w\n%s", path, err, out.String())
	}
	return Codec{Path: path, Package: clause, Types: names, Source: src}, nil
}

// preamble restates [Wire] where it is consumed, so an emitted package depends
// on the ZAP builder and nothing else. Both halves are required together: a type
// carrying one would encode from these constants and decode by reflection, which
// is two answers to where its layout lives.
const preamble = `// wire is what a type states when it answers for its own ZAP bytes.
type wire interface {
	MarshalZAP() ([]byte, error)
	UnmarshalZAP([]byte) error
}

`

// widening reads a list element whatever its width. An element is written at its
// own width, so a shorter one is zero-extended here and sign-extended by the
// caller — the rule the reflective decoder already follows.
const widening = `// wide reads a list element's little-endian bytes, whatever its width.
func wide(b []byte) uint64 {
	var buf [8]byte
	copy(buf[:], b)
	return binary.LittleEndian.Uint64(buf[:])
}

`

// emptying is the ZAP message a value with no slots crosses as. A type whose
// fields are all unexported — a time.Time, a netip.AddrPort — states nothing, so
// what crosses is a complete and empty object. It is what the reflective encoder
// writes for one, byte for byte.
const emptying = `// empty is the ZAP message a value with no slots crosses as.
func empty() []byte {
	b := zap.NewBuilder(zap.HeaderSize)
	b.StartObject(0).FinishAsRoot()
	return b.Finish()
}

`

func declare(w *bytes.Buffer, t reflect.Type, p *pkg) error {
	shape, err := LayoutOf(t)
	if err != nil {
		return fmt.Errorf("zip: %s: %w", goname(t), err)
	}
	name := t.Name()
	lo := lower(name)

	fmt.Fprintf(w, "// ---- %s %s\n\n", name, strings.Repeat("-", max(1, 66-len(name))))
	fmt.Fprint(w, "const (\n")
	for _, s := range shape.Slots {
		fmt.Fprintf(w, "\t%s%sAt = %d\n", lo, s.Name, s.Offset)
	}
	fmt.Fprintf(w, "\t%sSize = %d\n)\n\n", lo, shape.Size)
	fmt.Fprintf(w, "var _ wire = (*%s)(nil)\n\n", name)

	fmt.Fprintf(w, "// MarshalZAP writes %s from constant offsets.\n", name)
	fmt.Fprintf(w, "func (x *%s) MarshalZAP() ([]byte, error) {\n", name)
	// A nil receiver is an absent body, not an empty object — the answer the
	// reflective encoder gives, and a handler returning a nil *Out reaches here
	// as a non-nil interface holding one.
	fmt.Fprint(w, "\tif x == nil {\n\t\treturn nil, nil\n\t}\n")
	fmt.Fprintf(w, "\tb := zap.NewBuilder(%sSize + 256)\n", lo)
	for _, s := range shape.Slots {
		f, ok := held(t, s.Name)
		if !ok {
			continue
		}
		if kindOf(s, f.Type) == aList {
			if err := writeList(w, t, s, f, p); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(w, "\tob := b.StartObject(%sSize)\n", lo)
	for _, s := range shape.Slots {
		f, ok := held(t, s.Name)
		if !ok {
			continue
		}
		if kindOf(s, f.Type) == aList {
			continue
		}
		if err := writeField(w, lo, s, f, p); err != nil {
			return err
		}
	}
	for _, s := range shape.Slots {
		f, ok := held(t, s.Name)
		if !ok || kindOf(s, f.Type) != aList {
			continue
		}
		v := lower(s.Name)
		fmt.Fprintf(w, "\tif %sN > 0 {\n\t\tob.SetList(%s%sAt, %sAt, %sN)\n\t}\n", v, lo, s.Name, v, v)
	}
	fmt.Fprint(w, "\tob.FinishAsRoot()\n\treturn b.Finish(), nil\n}\n\n")

	fmt.Fprintf(w, "// UnmarshalZAP reads %s out of the buffer that arrived.\n", name)
	fmt.Fprintf(w, "func (x *%s) UnmarshalZAP(data []byte) error {\n", name)
	// An empty message leaves the value untouched: a void reply is an absence,
	// not a zero somebody might mistake for an answer.
	fmt.Fprint(w, "\tif x == nil || len(data) == 0 {\n\t\treturn nil\n\t}\n")
	fmt.Fprintf(w, "\tm, err := zap.Parse(data)\n\tif err != nil {\n\t\treturn fmt.Errorf(\"%s: %%w\", err)\n\t}\n", name)
	// The root is taken only if a field reads it. A message with no slot to
	// read still PARSES — an unreadable one is an error, not an empty value —
	// but Go refuses a variable nobody uses.
	var reads bytes.Buffer
	for _, s := range shape.Slots {
		f, ok := held(t, s.Name)
		if !ok {
			continue
		}
		if err := readField(&reads, lo, s, f, p); err != nil {
			return err
		}
	}
	if reads.Len() > 0 {
		fmt.Fprint(w, "\to := m.Root()\n")
		w.Write(reads.Bytes())
	} else {
		fmt.Fprint(w, "\t_ = m\n")
	}
	fmt.Fprint(w, "\treturn nil\n}\n\n")
	return nil
}

// The shapes a slot takes on this wire. Everything else is a scalar, which the
// setter table answers for by its .zap name.
type form int

const (
	aScalar form = iota
	aFixed
	aNest
	aList
)

func kindOf(s Slot, ft reflect.Type) form {
	ft = under(ft)
	switch {
	case ft.Kind() == reflect.Array:
		return aFixed
	case ft.Kind() == reflect.Struct:
		return aNest
	case ft.Kind() == reflect.Slice && strings.HasPrefix(s.Type, "list<"):
		return aList
	}
	return aScalar
}

// setter is the builder call for one .zap type and getter its reader; goType is
// what the reader hands back, for the conversion a defined type needs. They are
// one table so the two directions cannot name different widths.
var setter = map[string][3]string{
	"bool": {"SetBool", "Bool", "bool"},
	"i8":   {"SetInt8", "Int8", "int8"},
	"i16":  {"SetInt16", "Int16", "int16"},
	"i32":  {"SetInt32", "Int32", "int32"},
	"i64":  {"SetInt64", "Int64", "int64"},
	"u8":   {"SetUint8", "Uint8", "uint8"},
	"u16":  {"SetUint16", "Uint16", "uint16"},
	"u32":  {"SetUint32", "Uint32", "uint32"},
	"u64":  {"SetUint64", "Uint64", "uint64"},
	"f32":  {"SetFloat32", "Float32", "float32"},
	"f64":  {"SetFloat64", "Float64", "float64"},
	"text":  {"SetText", "Text", "string"},
	"bytes": {"SetBytes", "Bytes", "[]byte"},
}

func writeField(w *bytes.Buffer, lo string, s Slot, f reflect.StructField, p *pkg) error {
	at := lo + s.Name + "At"
	ref, tab, end := "x."+s.Name, "\t", ""
	if s.Ptr {
		// An absent pointer writes nothing: a null, not a zero, and the slot is
		// already zeroed.
		fmt.Fprintf(w, "\tif x.%s != nil {\n", s.Name)
		ref, tab, end = "(*x."+s.Name+")", "\t\t", "\t}\n"
	}
	switch kindOf(s, f.Type) {
	case aFixed:
		fmt.Fprintf(w, "%sob.SetBytesFixed(%s, %s[:])\n", tab, at, ref)
	case aNest:
		if empty(under(f.Type)) {
			fmt.Fprintf(w, "%sob.SetBytes(%s, empty())\n", tab, at)
			p.blank = true
			break
		}
		// x.F reaches the pointer method on its own: a field of a pointer
		// receiver is addressable, and a pointer field already is one.
		fmt.Fprintf(w, "%sinner%s, err := x.%s.MarshalZAP()\n%sif err != nil {\n%s\treturn nil, err\n%s}\n",
			tab, s.Name, s.Name, tab, tab, tab)
		fmt.Fprintf(w, "%sob.SetBytes(%s, inner%s)\n", tab, at, s.Name)
	default:
		c, ok := setter[s.Type]
		if !ok {
			return fmt.Errorf("zip: field %s: no wire form for %s", s.Name, s.Type)
		}
		fmt.Fprintf(w, "%sob.%s(%s, %s(%s))\n", tab, c[0], at, c[2], ref)
	}
	fmt.Fprint(w, end)
	return nil
}

func readField(w *bytes.Buffer, lo string, s Slot, f reflect.StructField, p *pkg) error {
	at := lo + s.Name + "At"
	ft := f.Type
	if s.Ptr {
		ft = under(ft)
	}
	switch kindOf(s, f.Type) {
	case aFixed:
		fmt.Fprintf(w, "\tcopy(x.%s[:], o.BytesFixed(%s, %d))\n", s.Name, at, s.N)
		return nil
	case aNest:
		if empty(under(f.Type)) {
			return nil // nothing crossed, so there is nothing to read back
		}
		fmt.Fprintf(w, "\tif raw := o.Bytes(%s); len(raw) > 0 {\n", at)
		if s.Ptr {
			fmt.Fprintf(w, "\t\tvar v %s\n\t\tif err := v.UnmarshalZAP(raw); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tx.%s = &v\n", p.spell(ft), s.Name)
		} else {
			fmt.Fprintf(w, "\t\tif err := x.%s.UnmarshalZAP(raw); err != nil {\n\t\t\treturn err\n\t\t}\n", s.Name)
		}
		fmt.Fprint(w, "\t}\n")
		return nil
	case aList:
		return readList(w, at, s, f, p)
	}
	c, ok := setter[s.Type]
	if !ok {
		return fmt.Errorf("zip: field %s: no wire form for %s", s.Name, s.Type)
	}
	spell := p.spell(ft)
	read := fmt.Sprintf("%s(o.%s(%s))", spell, c[1], at)
	switch s.Type {
	case "bytes":
		// A slice is not comparable, so an absent one is told by its length —
		// which is the same thing the reflective decoder asks when it decides
		// whether to allocate.
		if s.Ptr {
			fmt.Fprintf(w, "\tif raw := o.Bytes(%s); len(raw) > 0 {\n\t\tv := %s(append([]byte(nil), raw...))\n\t\tx.%s = &v\n\t}\n",
				at, spell, s.Name)
			return nil
		}
		read = fmt.Sprintf("%s(append([]byte(nil), o.Bytes(%s)...))", spell, at)
	case "text":
		// ZAP decodes a string zero-copy, over the frame that arrived, and the
		// next call on that connection reuses the frame. A reply that outlives
		// its call would mutate under its owner, so the codec copies here.
		read = fmt.Sprintf("%s(strings.Clone(o.Text(%s)))", spell, at)
		p.std("strings")
	}
	if s.Ptr {
		fmt.Fprintf(w, "\tif v := %s; %s {\n\t\tx.%s = &v\n\t}\n", read, present(s, spell), s.Name)
		return nil
	}
	fmt.Fprintf(w, "\tx.%s = %s\n", s.Name, read)
	return nil
}

// present is when an absent pointer field is actually there, mirroring the
// reflective decoder: it allocates only once something is, so a pointer to a
// zero value comes back nil rather than as a pointer to nothing.
func present(s Slot, spell string) string {
	switch s.Type {
	case "text":
		return `v != ""`
	case "bool":
		return "v"
	}
	return "v != " + spell + "(0)"
}

func writeList(w *bytes.Buffer, t reflect.Type, s Slot, f reflect.StructField, p *pkg) error {
	v := lower(s.Name)
	ref := "x." + s.Name
	if s.Ptr {
		ref = "(*x." + s.Name + ")"
	}
	et := under(f.Type).Elem()
	ptr := et.Kind() == reflect.Pointer
	el := under(et)

	var elem bytes.Buffer
	w, outer := &elem, w
	switch {
	case el.Kind() == reflect.Struct && empty(el):
		fmt.Fprint(w, "\t\t\tenc := empty()\n")
		p.blank = true
	case el.Kind() == reflect.Struct:
		// A nil element encodes as the zero value, which is what the reflective
		// encoder writes for one: a list has no hole to leave.
		if ptr {
			fmt.Fprintf(w, "\t\t\telem := %s[i]\n\t\t\tif elem == nil {\n\t\t\t\telem = new(%s)\n\t\t\t}\n", ref, p.spell(el))
			fmt.Fprint(w, "\t\t\tenc, err := elem.MarshalZAP()\n\t\t\tif err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n")
		} else {
			fmt.Fprintf(w, "\t\t\tenc, err := %s[i].MarshalZAP()\n\t\t\tif err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n", ref)
		}
	case el.Kind() == reflect.Array:
		fmt.Fprintf(w, "\t\t\tenc := %s[i][:]\n", ref)
	case s.Elem == "text" || s.Elem == "bytes":
		fmt.Fprintf(w, "\t\t\tenc := []byte(%s[i])\n", ref)
	case s.Elem == "bool":
		fmt.Fprintf(w, "\t\t\tenc := []byte{0}\n\t\t\tif %s[i] {\n\t\t\t\tenc[0] = 1\n\t\t\t}\n", ref)
	default:
		c, ok := setter[s.Elem]
		if !ok {
			return fmt.Errorf("zip: field %s: no wire form for list element %s", s.Name, s.Elem)
		}
		_ = c
		fmt.Fprint(w, "\t\t\tvar full [8]byte\n")
		fmt.Fprintf(w, "\t\t\tbinary.LittleEndian.PutUint64(full[:], %s)\n", element(s, ref))
		fmt.Fprintf(w, "\t\t\tenc := full[:%d]\n", widthOf(s.Elem))
		if s.Elem == "f32" || s.Elem == "f64" {
			p.std("math")
		}
	}
	fmt.Fprint(w, "\t\t\tvar n [4]byte\n\t\t\tbinary.LittleEndian.PutUint32(n[:], uint32(len(enc)))\n")
	fmt.Fprint(w, "\t\t\tblob = append(blob, n[:]...)\n\t\t\tblob = append(blob, enc...)\n\t\t}\n")

	w = outer
	if s.Ptr {
		// An absent pointer writes nothing, and its length cannot be asked for.
		fmt.Fprintf(w, "\t%sAt, %sN := 0, 0\n\tif x.%s != nil {\n\t\t%sN = len(%s)\n\t}\n", v, v, s.Name, v, ref)
	} else {
		fmt.Fprintf(w, "\t%sAt, %sN := 0, len(%s)\n", v, v, ref)
	}
	fmt.Fprintf(w, "\tif %sN > 0 {\n\t\tvar blob []byte\n\t\tfor %s range %s {\n", v, index(&elem), ref)
	w.Write(elem.Bytes())
	fmt.Fprintf(w, "\t\t%sAt = b.WriteBytes(blob)\n\t}\n", v)
	p.std("encoding/binary")
	return nil
}

// index is the loop variable a list body needs, and nothing when it needs none:
// an element with no slots reads no index, and Go refuses a variable nobody uses.
func index(body *bytes.Buffer) string {
	if bytes.Contains(body.Bytes(), []byte("[i]")) || bytes.Contains(body.Bytes(), []byte("(i)")) {
		return "i :="
	}
	return ""
}

// element spells one list element for the little-endian write. A float crosses
// as its bits, which is what the reflective encoder writes, so the two agree.
func element(s Slot, ref string) string {
	switch s.Elem {
	case "f32":
		return fmt.Sprintf("uint64(math.Float32bits(float32(%s[i])))", ref)
	case "f64":
		return fmt.Sprintf("math.Float64bits(float64(%s[i]))", ref)
	}
	return fmt.Sprintf("uint64(%s[i])", ref)
}

func readList(w *bytes.Buffer, at string, s Slot, f reflect.StructField, p *pkg) error {
	ft := under(f.Type)
	et := ft.Elem()
	ptr := et.Kind() == reflect.Pointer
	el := under(et)

	var elem bytes.Buffer
	w, outer := &elem, w
	switch {
	case el.Kind() == reflect.Struct && empty(el):
		if ptr {
			fmt.Fprintf(w, "\t\t\trows[i] = new(%s)\n", p.spell(el))
		}
	case el.Kind() == reflect.Struct:
		if ptr {
			fmt.Fprintf(w, "\t\t\trows[i] = new(%s)\n", p.spell(el))
		}
		fmt.Fprint(w, "\t\t\tif err := rows[i].UnmarshalZAP(l.BytesAt(i)); err != nil {\n\t\t\t\treturn err\n\t\t\t}\n")
	case el.Kind() == reflect.Array:
		fmt.Fprint(w, "\t\t\tcopy(rows[i][:], l.BytesAt(i))\n")
	case s.Elem == "text":
		fmt.Fprintf(w, "\t\t\trows[i] = %s(l.BytesAt(i))\n", p.spell(et))
	case s.Elem == "bytes":
		fmt.Fprintf(w, "\t\t\trows[i] = %s(append([]byte(nil), l.BytesAt(i)...))\n", p.spell(et))
	case s.Elem == "bool":
		fmt.Fprintf(w, "\t\t\traw := l.BytesAt(i)\n\t\t\trows[i] = %s(len(raw) > 0 && raw[0] != 0)\n", p.spell(et))
	default:
		fmt.Fprintf(w, "\t\t\trows[i] = %s(%s)\n", p.spell(et), elementRead(s))
		p.std("encoding/binary")
		p.wide = true
		if s.Elem == "f32" || s.Elem == "f64" {
			p.std("math")
		}
	}
	w = outer
	fmt.Fprintf(w, "\tif l := o.List(%s); l.Len() > 0 {\n", at)
	fmt.Fprintf(w, "\t\trows := make(%s, l.Len())\n\t\tfor %s range rows {\n", p.spell(ft), index(&elem))
	w.Write(elem.Bytes())
	assign := "x." + s.Name + " = rows"
	if s.Ptr {
		assign = "x." + s.Name + " = &rows"
	}
	fmt.Fprintf(w, "\t\t}\n\t\t%s\n\t}\n", assign)
	return nil
}

func elementRead(s Slot) string {
	n := widthOf(s.Elem)
	switch s.Elem {
	case "i8", "i16", "i32", "i64":
		// Sign-extend from the element's own width, which is what the reflective
		// decoder does: a negative int32 read as a uint64 is a very large number.
		return fmt.Sprintf("int64(wide(l.BytesAt(i))<<%d) >> %d", 64-8*n, 64-8*n)
	case "f32":
		return "math.Float32frombits(uint32(wide(l.BytesAt(i))))"
	case "f64":
		return "math.Float64frombits(wide(l.BytesAt(i)))"
	}
	return "wide(l.BytesAt(i))"
}

func widthOf(zaptype string) int {
	switch zaptype {
	case "bool", "i8", "u8":
		return 1
	case "i16", "u16":
		return 2
	case "i32", "u32", "f32":
		return 4
	}
	return 8
}

func lower(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
