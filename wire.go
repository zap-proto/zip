package zip

import (
	"reflect"

	"github.com/zap-proto/zip/internal/zapenc"
)

// Wire is implemented by a type that states its own ZAP wire form instead of
// having one derived from it.
//
// The op-call plane carries In and Out as ZAP (see [Call]), and derives the
// layout from the Go type: fields take slots in declaration order, each aligned
// to its own width, and a field IS its offset. The derivation is cached; the
// COPY is not. Every call reflects over the value, sizes a builder by guess and
// walks the fields again — work whose answer was fixed the moment the type was
// declared.
//
//	type Started struct {
//	    Addr  string `json:"addr"  zap:"0"`
//	    Known bool   `json:"known" zap:"8"`
//	}
//
//	func (s *Started) MarshalZAP() ([]byte, error) { … ob.SetText(0, s.Addr) … }
//	func (s *Started) UnmarshalZAP(b []byte) error { … s.Addr = o.Text(0) … }
//
// The two methods are what a hand-written ZAP codec is, with the offsets as
// constants. They are meant to be GENERATED from the same `zap:` tags a .zap
// schema is generated from, so one declaration answers for the wire, the schema
// and the code, and the derivation happens at build time where its cost is paid
// once.
//
// Both methods are required together. A type carrying only one would encode from
// generated offsets and decode by reflection — two answers to where its layout
// lives, and the pair would disagree the first time a field moved.
//
// It also carries what the derivation cannot express. A fixed-size array is
// refused outright by the reflective encoder, so an ids.ID — [32]byte, the
// bytes_fixed[32] of a .zap schema — cannot cross the plane at all today. A type
// implementing Wire writes it inline (zap.ObjectBuilder.SetBytesFixed) and reads
// it back as a slice of the buffer that arrived (zap.Object.BytesFixed).
//
// The compatibility rule does not change: the layout is still the type, so
// reordering, inserting or retyping a field changes the wire for every peer.
// Append at the end, and only at the end.
type Wire interface {
	MarshalZAP() ([]byte, error)
	UnmarshalZAP([]byte) error
}

// Slot is where one field of a ZAP message sits on the wire, and what it is.
// See [LayoutOf].
type Slot = zapenc.Slot

// Shape is a whole ZAP message: its slots in declaration order, and its size.
type Shape = zapenc.Shape

// LayoutOf derives the wire layout of a struct type: which offset each field
// takes, how wide it is, and what the IDL calls it.
//
// It is ONE derivation with three readers, and that is the whole reason it is
// exported. The op-call plane ENCODES against it; a .zap schema generated from
// these types must STATE it, field by field, as `Name type @Offset`; and a code
// generator emitting a [Wire] implementation must EMIT it as constants. Derived
// a second time, those three describe three different wires and only one of them
// is spoken — a schema is then a document about a protocol nobody runs, and a
// generated codec is a rolling deploy that reads its own messages and nobody
// else's.
//
// Two details a second derivation gets wrong, both measured on this fleet's own
// types. Offsets are ALIGNED, not packed: a field begins at a multiple of its own
// width, so a bool at 0 puts the string after it at 8 and not at 1. And a nested
// value is EIGHT bytes and its IDL type is `bytes`, because it crosses as a
// complete ZAP message in an {offset,length} slot rather than as a four-byte
// relative offset. Packing, or giving a nested value four bytes, moves every
// field after it.
//
// bytes_fixed[N] is laid out here and is NOT carried by the reflective codec. The
// layout is what a schema and a generator need; refusing to encode it is what
// makes an id — an ids.ID is [32]byte — force a type to declare its own wire
// rather than quietly acquiring a reflective one.
func LayoutOf(t reflect.Type) (Shape, error) { return zapenc.LayoutOf(t) }
