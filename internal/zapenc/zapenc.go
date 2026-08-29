// Package zapenc encodes a Go value as a ZAP message and reads one back.
//
// It is what makes the op-call plane ZAP end to end. An op's In and Out are
// ordinary Go structs; this derives their wire layout from the type itself, so
// a service-to-service call carries the same bytes in memory that it puts on
// the socket, with no second encoding in the middle. JSON stays where it
// belongs — at the boundary, for browsers — and never inside the binary
// protocol.
//
// # The layout is the type
//
// Fields take slots in declaration order, each aligned to its own width, which
// is the rule zap's own schema builder applies. Nothing is named on the wire:
// a field IS its offset, exactly as a hand-written ZAP codec would spell it,
// and the type declaration is the schema both ends read. That is also the whole
// compatibility rule — REORDERING, INSERTING OR RETYPING A FIELD CHANGES THE
// WIRE. Append at the end, and only at the end.
//
// # What crosses
//
// bool, every sized int/uint, float32/64, string, []byte, nested structs,
// pointers to any of those, and slices. A slice writes each element
// length-prefixed, which is the variable-element list layout zap.List.BytesAt
// and ObjectAt already read; a slice of structs therefore carries one complete
// ZAP message per element.
//
// Anything else — a map, an interface, a channel, a FIXED-SIZE ARRAY — is
// refused at encode rather than dropped. A field that silently does not cross is
// the failure this package exists to make impossible.
//
// # A type that owns its wire is not reflected over
//
// Deriving the layout is cached; COPYING THE FIELDS IS NOT. Every call runs
// reflect.ValueOf, sizes a builder by guess (lay.size+256) and walks the fields
// again, so the cost is paid per call for a fact that is fixed at compile time.
//
// So a type may state its own wire form — [zip.Wire], MarshalZAP/UnmarshalZAP —
// and both entry points below take that answer BEFORE they reach for reflect.
// Generated code emits those two methods with the offsets as constants, which is
// what a hand-written ZAP codec is, so the derivation moves to build time and the
// call path holds none of it.
//
// It is also the only way to carry what the derivation cannot express. A
// bytes_fixed[N] field — an ids.ID is [32]byte — is refused here and written
// inline by generated code through zap.Object.BytesFixed, so a round trip that
// SUCCEEDS for such a type is proof the reflective path was not taken.
package zapenc

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"sync"

	zap "github.com/zap-proto/go"
)

// wire is [zip.Wire] restated where it is consumed, so this package depends on
// nothing to recognise it. Both methods are required together: a type that
// carried only one would encode from generated offsets and decode by reflection,
// which is two answers to where its layout lives.
type wire interface {
	MarshalZAP() ([]byte, error)
	UnmarshalZAP([]byte) error
}

// Marshal encodes v as a ZAP message. v must be a struct or a pointer to one:
// the message's root is an object, and there is nothing for a bare scalar to be
// the root of.
//
// A v that states its own wire form answers for itself and nothing here reflects.
func Marshal(v any) ([]byte, error) {
	if w, ok := v.(wire); ok {
		return w.MarshalZAP()
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil // a nil input is an absent body, not an empty object
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("zapenc: %s is not a struct", rv.Type())
	}
	lay, err := layoutOf(rv.Type())
	if err != nil {
		return nil, err
	}
	b := zap.NewBuilder(lay.size + 256)
	if err := writeStruct(b, lay, rv, true); err != nil {
		return nil, err
	}
	return b.Finish(), nil
}

// Unmarshal reads a ZAP message into v, which must be a non-nil pointer to a
// struct. An empty message leaves v untouched — a void reply is an absence, not
// a zero value that a caller might mistake for an answer.
func Unmarshal(data []byte, v any) error {
	if len(data) == 0 {
		return nil
	}
	if w, ok := v.(wire); ok {
		return w.UnmarshalZAP(data)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("zapenc: Unmarshal needs a non-nil pointer, got %T", v)
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("zapenc: %s is not a struct", rv.Type())
	}
	msg, err := zap.Parse(data)
	if err != nil {
		return fmt.Errorf("zapenc: %w", err)
	}
	lay, err := layoutOf(rv.Type())
	if err != nil {
		return err
	}
	return readStruct(msg.Root(), lay, rv)
}

// ---- layout ---------------------------------------------------------------

type kind uint8

const (
	kBool kind = iota
	kInt8
	kInt16
	kInt32
	kInt64
	kUint8
	kUint16
	kUint32
	kUint64
	kFloat32
	kFloat64
	kText
	kBytes
	kStruct
	kSlice
	// kFixed is bytes_fixed[N] — N bytes written INLINE, aligned to 1. It has a
	// layout and no reflective codec: see [Layout] and writeStruct's refusal.
	kFixed
)

type field struct {
	index  int
	offset int
	kind   kind
	n      int          // kFixed: the array length
	ptr    bool         // the Go field is a pointer to the encoded type
	elem   *layout      // kStruct: the nested layout
	slice  *sliceElem   // kSlice: how one element is encoded
	typ    reflect.Type // the Go type at this field, minus any pointer
}

// width is the field's slot in the fixed area. Everything variable — text,
// bytes, a list, a nested value — carries an offset and a length in 8 bytes;
// bytes_fixed[N] is its N bytes, inline.
func (f field) width() int {
	if f.kind == kFixed {
		return f.n
	}
	return width(f.kind)
}

// align is where the slot may begin. It is the slot's own width, except for
// bytes_fixed: those bytes are inline and align to 1, so a 32-byte id follows a
// u32 at offset 4 rather than at 32 — which is what a .zap schema writes.
func (f field) align() int {
	if f.kind == kFixed {
		return 1
	}
	return width(f.kind)
}

type sliceElem struct {
	kind kind
	elem *layout      // when the element is a struct
	typ  reflect.Type // the element type, minus any pointer
	ptr  bool
}

type layout struct {
	fields []field
	size   int
}

var layouts sync.Map // reflect.Type -> *layout

func layoutOf(t reflect.Type) (*layout, error) {
	if v, ok := layouts.Load(t); ok {
		return v.(*layout), nil
	}
	lay, err := buildLayout(t)
	if err != nil {
		return nil, err
	}
	layouts.Store(t, lay)
	return lay, nil
}

func buildLayout(t reflect.Type) (*layout, error) {
	lay := &layout{}
	off := 0
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			// An unexported field cannot be read or written by reflection, so it
			// cannot cross. Skipping it silently is right — it was never part of
			// the contract — but it must not consume a slot either.
			continue
		}
		f, err := fieldOf(sf.Type)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name(), sf.Name, err)
		}
		f.index = i
		off = align(off, f.align())
		f.offset = off
		off += f.width()
		lay.fields = append(lay.fields, f)
	}
	lay.size = align(off, 8)
	return lay, nil
}

func fieldOf(t reflect.Type) (field, error) {
	f := field{}
	if t.Kind() == reflect.Pointer {
		f.ptr = true
		t = t.Elem()
	}
	f.typ = t
	switch t.Kind() {
	case reflect.Bool:
		f.kind = kBool
	case reflect.Int8:
		f.kind = kInt8
	case reflect.Int16:
		f.kind = kInt16
	case reflect.Int32:
		f.kind = kInt32
	case reflect.Int, reflect.Int64:
		f.kind = kInt64
	case reflect.Uint8:
		f.kind = kUint8
	case reflect.Uint16:
		f.kind = kUint16
	case reflect.Uint32:
		f.kind = kUint32
	case reflect.Uint, reflect.Uint64:
		f.kind = kUint64
	case reflect.Float32:
		f.kind = kFloat32
	case reflect.Float64:
		f.kind = kFloat64
	case reflect.String:
		f.kind = kText
	case reflect.Struct:
		f.kind = kStruct
		lay, err := layoutOf(t)
		if err != nil {
			return f, err
		}
		f.elem = lay
	case reflect.Array:
		// bytes_fixed[N]. Only a byte array: an array of anything else has no
		// inline wire form, and a list is how a sequence crosses.
		if t.Elem().Kind() != reflect.Uint8 {
			return f, fmt.Errorf("zapenc: an array of %s has no wire form; use a slice", t.Elem())
		}
		f.kind, f.n = kFixed, t.Len()
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			f.kind = kBytes
			break
		}
		f.kind = kSlice
		se, err := sliceElemOf(t.Elem())
		if err != nil {
			return f, err
		}
		f.slice = se
	default:
		return f, fmt.Errorf("zapenc: %s cannot cross the plane; give it a type that can", t.Kind())
	}
	return f, nil
}

func sliceElemOf(t reflect.Type) (*sliceElem, error) {
	se := &sliceElem{}
	if t.Kind() == reflect.Pointer {
		se.ptr = true
		t = t.Elem()
	}
	se.typ = t
	f, err := fieldOf(t)
	if err != nil {
		return nil, err
	}
	se.kind = f.kind
	se.elem = f.elem
	if se.kind == kSlice {
		return nil, fmt.Errorf("zapenc: a slice of slices has no wire form; wrap the inner one in a struct")
	}
	return se, nil
}

// width is the fixed-area size of one field. Everything variable — text, bytes,
// slices and nested structs — carries an offset and a length.
func width(k kind) int {
	switch k {
	case kBool, kInt8, kUint8:
		return 1
	case kInt16, kUint16:
		return 2
	case kInt32, kUint32, kFloat32:
		return 4
	case kInt64, kUint64, kFloat64:
		return 8
	case kText, kBytes, kSlice, kStruct:
		return 8
	}
	return 8
}

func align(off, n int) int { return (off + n - 1) &^ (n - 1) }

// ---- encode ---------------------------------------------------------------

func writeStruct(b *zap.Builder, lay *layout, rv reflect.Value, asRoot bool) error {
	// Nested values and list elements must be written BEFORE the object that
	// points at them, because SetObject/SetList take an offset that already
	// exists. Collect them first, then fill the fixed area.
	type pending struct {
		f      field
		list   int
		length int
	}
	var pends []pending
	for _, f := range lay.fields {
		fv := rv.Field(f.index)
		if f.ptr {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		switch f.kind {
		case kSlice:
			if fv.Len() == 0 {
				continue
			}
			off, n, err := writeList(b, f.slice, fv)
			if err != nil {
				return err
			}
			pends = append(pends, pending{f: f, list: off, length: n})
		}
	}

	ob := b.StartObject(lay.size)
	for _, f := range lay.fields {
		fv := rv.Field(f.index)
		if f.ptr {
			if fv.IsNil() {
				continue // an absent pointer writes nothing: a null, not a zero
			}
			fv = fv.Elem()
		}
		switch f.kind {
		case kBool:
			ob.SetBool(f.offset, fv.Bool())
		case kInt8:
			ob.SetInt8(f.offset, int8(fv.Int()))
		case kInt16:
			ob.SetInt16(f.offset, int16(fv.Int()))
		case kInt32:
			ob.SetInt32(f.offset, int32(fv.Int()))
		case kInt64:
			ob.SetInt64(f.offset, fv.Int())
		case kUint8:
			ob.SetUint8(f.offset, uint8(fv.Uint()))
		case kUint16:
			ob.SetUint16(f.offset, uint16(fv.Uint()))
		case kUint32:
			ob.SetUint32(f.offset, uint32(fv.Uint()))
		case kUint64:
			ob.SetUint64(f.offset, fv.Uint())
		case kFloat32:
			ob.SetFloat32(f.offset, float32(fv.Float()))
		case kFloat64:
			ob.SetFloat64(f.offset, fv.Float())
		case kText:
			ob.SetText(f.offset, fv.String())
		case kBytes:
			ob.SetBytes(f.offset, fv.Bytes())
		case kStruct:
			// A nested value is a complete ZAP message stored as bytes, so a reader
			// Parses it on its own — the same rule list elements follow, rather
			// than a second one for the singular case.
			inner, err := marshalStruct(f.elem, fv)
			if err != nil {
				return err
			}
			ob.SetBytes(f.offset, inner)
		case kFixed:
			return fixedRefusal(fv.Type())
		}
	}
	for _, p := range pends {
		ob.SetList(p.f.offset, p.list, p.length)
	}
	if asRoot {
		ob.FinishAsRoot()
	} else {
		ob.Finish()
	}
	return nil
}

// writeList lays out a variable-element list: each element length-prefixed with
// a little-endian uint32, which is the layout zap.List.BytesAt and ObjectAt
// read. The returned length is the ELEMENT COUNT, never a byte count.
func writeList(b *zap.Builder, se *sliceElem, rv reflect.Value) (int, int, error) {
	var blob []byte
	for i := 0; i < rv.Len(); i++ {
		ev := rv.Index(i)
		if se.ptr {
			if ev.IsNil() {
				ev = reflect.Zero(se.typ)
			} else {
				ev = ev.Elem()
			}
		}
		enc, err := marshalElem(se, ev)
		if err != nil {
			return 0, 0, err
		}
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(enc)))
		blob = append(blob, n[:]...)
		blob = append(blob, enc...)
	}
	return b.WriteBytes(blob), rv.Len(), nil
}

// marshalElem encodes one list element. A struct element is a complete ZAP
// message; a scalar element is its own little-endian bytes, and text is its
// UTF-8 — so an element is always self-describing given the field's type.
func marshalElem(se *sliceElem, ev reflect.Value) ([]byte, error) {
	switch se.kind {
	case kStruct:
		return marshalStruct(se.elem, ev)
	case kText:
		return []byte(ev.String()), nil
	case kBytes:
		return ev.Bytes(), nil
	case kBool:
		if ev.Bool() {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case kInt8, kInt16, kInt32, kInt64:
		return leInt(uint64(ev.Int()), width(se.kind)), nil
	case kUint8, kUint16, kUint32, kUint64:
		return leInt(ev.Uint(), width(se.kind)), nil
	case kFloat32:
		return leInt(uint64(f32bits(float32(ev.Float()))), 4), nil
	case kFloat64:
		return leInt(f64bits(ev.Float()), 8), nil
	}
	return nil, fmt.Errorf("zapenc: list element of kind %d has no wire form", se.kind)
}

func marshalStruct(lay *layout, rv reflect.Value) ([]byte, error) {
	b := zap.NewBuilder(lay.size + 128)
	if err := writeStruct(b, lay, rv, true); err != nil {
		return nil, err
	}
	return b.Finish(), nil
}

func leInt(v uint64, n int) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append([]byte(nil), buf[:n]...)
}

// ---- decode ---------------------------------------------------------------

func readStruct(o zap.Object, lay *layout, rv reflect.Value) error {
	for _, f := range lay.fields {
		fv := rv.Field(f.index)
		target := fv
		if f.ptr {
			// Allocate only once something is actually there, so an absent field
			// reads back as nil rather than as a pointer to a zero value.
			nv := reflect.New(f.typ)
			target = nv.Elem()
			if err := readField(o, f, target); err != nil {
				return err
			}
			if !target.IsZero() {
				fv.Set(nv)
			}
			continue
		}
		if err := readField(o, f, target); err != nil {
			return err
		}
	}
	return nil
}

func readField(o zap.Object, f field, target reflect.Value) error {
	switch f.kind {
	case kBool:
		target.SetBool(o.Bool(f.offset))
	case kInt8:
		target.SetInt(int64(o.Int8(f.offset)))
	case kInt16:
		target.SetInt(int64(o.Int16(f.offset)))
	case kInt32:
		target.SetInt(int64(o.Int32(f.offset)))
	case kInt64:
		target.SetInt(o.Int64(f.offset))
	case kUint8:
		target.SetUint(uint64(o.Uint8(f.offset)))
	case kUint16:
		target.SetUint(uint64(o.Uint16(f.offset)))
	case kUint32:
		target.SetUint(uint64(o.Uint32(f.offset)))
	case kUint64:
		target.SetUint(o.Uint64(f.offset))
	case kFloat32:
		target.SetFloat(float64(o.Float32(f.offset)))
	case kFloat64:
		target.SetFloat(o.Float64(f.offset))
	case kText:
		target.SetString(o.Text(f.offset))
	case kBytes:
		if b := o.Bytes(f.offset); len(b) > 0 {
			target.SetBytes(append([]byte(nil), b...))
		}
	case kStruct:
		raw := o.Bytes(f.offset)
		if len(raw) == 0 {
			return nil // absent nested value: leave the zero struct
		}
		msg, err := zap.Parse(raw)
		if err != nil {
			return fmt.Errorf("zapenc: nested: %w", err)
		}
		return readStruct(msg.Root(), f.elem, target)
	case kSlice:
		return readList(o, f, target)
	case kFixed:
		return fixedRefusal(f.typ)
	}
	return nil
}

// fixedRefusal is what the reflective path answers for bytes_fixed[N].
//
// The LAYOUT knows the shape — [Layout] gives it an offset and a width, which is
// what a schema and a code generator need — and the reflective codec deliberately
// does not carry it. Reflection is what this whole seam exists to leave, so making
// it more capable is the wrong direction: an id is exactly the case that should
// force a type to declare its own wire, and a type that has done so never reaches
// this line.
func fixedRefusal(t reflect.Type) error {
	return fmt.Errorf("zapenc: %s is bytes_fixed[%d], which the reflective codec does not carry; "+
		"declare the type's wire (MarshalZAP/UnmarshalZAP) and it crosses inline", t, t.Len())
}

func readList(o zap.Object, f field, target reflect.Value) error {
	l := o.List(f.offset)
	n := l.Len()
	if n == 0 {
		return nil
	}
	out := reflect.MakeSlice(target.Type(), n, n)
	for i := 0; i < n; i++ {
		ev := out.Index(i)
		if f.slice.ptr {
			nv := reflect.New(f.slice.typ)
			if err := readElem(l, i, f.slice, nv.Elem()); err != nil {
				return err
			}
			ev.Set(nv)
			continue
		}
		if err := readElem(l, i, f.slice, ev); err != nil {
			return err
		}
	}
	target.Set(out)
	return nil
}

func readElem(l zap.List, i int, se *sliceElem, target reflect.Value) error {
	if se.kind == kStruct {
		return readStruct(l.ObjectAt(i), se.elem, target)
	}
	raw := l.BytesAt(i)
	switch se.kind {
	case kText:
		target.SetString(string(raw))
	case kBytes:
		target.SetBytes(append([]byte(nil), raw...))
	case kBool:
		target.SetBool(len(raw) > 0 && raw[0] != 0)
	case kInt8, kInt16, kInt32, kInt64:
		target.SetInt(signed(raw, width(se.kind)))
	case kUint8, kUint16, kUint32, kUint64:
		target.SetUint(unsigned(raw))
	case kFloat32:
		target.SetFloat(float64(f32from(uint32(unsigned(raw)))))
	case kFloat64:
		target.SetFloat(f64from(unsigned(raw)))
	}
	return nil
}

func unsigned(b []byte) uint64 {
	var buf [8]byte
	copy(buf[:], b)
	return binary.LittleEndian.Uint64(buf[:])
}

func signed(b []byte, n int) int64 {
	v := unsigned(b)
	shift := uint(64 - 8*n)
	return int64(v<<shift) >> shift // sign-extend from the field's own width
}

func f32bits(f float32) uint32 { return math.Float32bits(f) }
func f64bits(f float64) uint64 { return math.Float64bits(f) }
func f32from(u uint32) float32 { return math.Float32frombits(u) }
func f64from(u uint64) float64 { return math.Float64frombits(u) }

// ---- the layout, for whoever needs to write it down -------------------------

// Slot is where one field of a message sits on the wire, and what it is.
//
// It is what a .zap schema states as `Name type @Offset` and what a generated
// codec states as a Go constant. Both are projections of the SAME derivation the
// reflective codec encodes against, which is the point: the schema, the generated
// code and the running encoder cannot describe three wires.
type Slot struct {
	Name   string // the Go field name
	Offset int
	Width  int
	Type   string // the .zap type: u64, text, bytes, bytes_fixed[32], list<…>, struct
	N      int    // bytes_fixed[N]: the length. Otherwise 0.
	Ptr    bool   // the Go field is a pointer to the encoded type
	Elem   string // list<…>: the element's .zap type
}

// Shape is a whole message: its slots in declaration order, and its size.
type Shape struct {
	Slots []Slot
	Size  int
}

// LayoutOf derives the wire layout of a struct type.
//
// A field is aligned to its own width and takes that many bytes; the size is
// rounded up to 8. bytes_fixed[N] is the exception and the reason alignment is
// stated apart from width: it is N bytes INLINE, aligned to 1, so an id sits
// immediately after the u32 before it.
//
// This is the derivation Marshal encodes against, so a schema or a generator
// reading it describes the wire that is actually spoken. Deriving it a second
// time — packing the offsets, or giving a nested value four bytes instead of the
// eight it takes as bytes — produces a document about a wire nobody speaks.
func LayoutOf(t reflect.Type) (Shape, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return Shape{}, fmt.Errorf("zapenc: %s is not a struct", t)
	}
	lay, err := layoutOf(t)
	if err != nil {
		return Shape{}, err
	}
	sh := Shape{Size: lay.size}
	for _, f := range lay.fields {
		s := Slot{
			Name:   t.Field(f.index).Name,
			Offset: f.offset,
			Width:  f.width(),
			Type:   spell(f.kind, f.n),
			N:      f.n,
			Ptr:    f.ptr,
		}
		if f.kind == kSlice && f.slice != nil {
			s.Elem = spell(f.slice.kind, 0)
			s.Type = "list<" + s.Elem + ">"
		}
		sh.Slots = append(sh.Slots, s)
	}
	return sh, nil
}

// spell is the .zap name of a kind. A nested value is "bytes" and not "struct",
// because that is what it IS on this wire: writeStruct carries it through
// SetBytes as a complete ZAP message in an 8-byte {offset,length} slot, never
// through SetObject's 4-byte relative offset.
func spell(k kind, n int) string {
	switch k {
	case kBool:
		return "bool"
	case kInt8:
		return "i8"
	case kInt16:
		return "i16"
	case kInt32:
		return "i32"
	case kInt64:
		return "i64"
	case kUint8:
		return "u8"
	case kUint16:
		return "u16"
	case kUint32:
		return "u32"
	case kUint64:
		return "u64"
	case kFloat32:
		return "f32"
	case kFloat64:
		return "f64"
	case kText:
		return "text"
	case kBytes, kStruct:
		return "bytes"
	case kFixed:
		return "bytes_fixed[" + strconv.Itoa(n) + "]"
	}
	return "bytes"
}
