// Package codec holds the types that [zip.Codecs] is proved against.
//
// The proof it exists for is byte equality: for a type the REFLECTIVE encoder
// can write, the generated codec must write the same bytes and read back the
// same value. That is what lets a fleet roll a generated codec out one pod at a
// time — the pod on the old path and the pod on the new one are speaking one
// wire, not two — and it is the property a second derivation of the layout would
// silently break.
//
// The fixtures cover every slot form the emitter has a branch for, plus the one
// the reflective encoder refuses outright: bytes_fixed[N], where there are no
// bytes to compare against because there are none at all.
package codec

// Count is a defined scalar, which needs a conversion on the way in and out.
type Count uint64

// Leaf is a nested value and a list element.
type Leaf struct {
	N uint32
	S string
}

// Trunk carries one field of every form the emitter writes.
type Trunk struct {
	B    bool
	I8   int8
	I16  int16
	I32  int32
	I64  int64
	U8   uint8
	U16  uint16
	U32  uint32
	U64  uint64
	F32  float32
	F64  float64
	C    Count
	Text string
	Raw  []byte
	Leaf Leaf
	PtrU *uint64
	PtrS *string
	PtrB *bool
	PtrL *Leaf
	Nums []uint32
	Sigs []int32
	Bigs []float64
	Strs []string
	Bufs [][]byte
	Bits []bool
	Kids []Leaf
	Ptrs []*Leaf
}

// Ided is what the reflective encoder refuses: an id is [32]byte, and a fixed
// array has a layout the reflective codec deliberately does not carry.
type Ided struct {
	ID   [32]byte
	Name string
	IDs  [][32]byte
	Leaf Leaf
}

// Loose is Ided with no codec generated for it, which is what the reflective
// encoder is left holding when a type has not stated its wire.
type Loose struct {
	ID   [32]byte
	Name string
}
