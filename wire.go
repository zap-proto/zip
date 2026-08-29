package zip

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
