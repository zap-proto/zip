package layout

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/zap-proto/zip/internal/zapenc"
)

// TestTheCodecKeepsTheWire is the whole reason the offsets are read from one
// derivation instead of computed a second time. A generated codec goes out one
// pod at a time, so the pod that has it and the pod that has not are speaking
// the same wire — or they are speaking two, and the second one is a silent
// mis-read of every field after the first that moved.
func TestTheCodecKeepsTheWire(t *testing.T) {
	for _, v := range trunks() {
		was, err := zapenc.Marshal(&v)
		if err != nil {
			t.Fatalf("reflective: %v", err)
		}
		is, err := v.MarshalZAP()
		if err != nil {
			t.Fatalf("generated: %v", err)
		}
		if !bytes.Equal(was, is) {
			t.Fatalf("the generated codec moved the wire\n reflective %d bytes: % x\n generated  %d bytes: % x",
				len(was), was, len(is), is)
		}
	}
}

// TestEitherSideReadsTheOther holds the decode direction: bytes written by one
// encoder read back through the other into the same value, both ways round.
func TestEitherSideReadsTheOther(t *testing.T) {
	for _, v := range trunks() {
		enc, err := v.MarshalZAP()
		if err != nil {
			t.Fatal(err)
		}
		var byReflection Trunk
		if err := zapenc.Unmarshal(enc, &byReflection); err != nil {
			t.Fatal(err)
		}
		var byCodec Trunk
		if err := byCodec.UnmarshalZAP(enc); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(byReflection, byCodec) {
			t.Fatalf("the two decoders disagree\n reflective %+v\n generated  %+v", byReflection, byCodec)
		}
	}
}

// TestAnIDCrossesOnlyWithACodec is the forcing function stated as a test. The
// reflective encoder refuses bytes_fixed[N] — an ids.ID is [32]byte — and the
// same value with a codec goes out and comes back whole.
func TestAnIDCrossesOnlyWithACodec(t *testing.T) {
	v := Ided{
		ID:   [32]byte{0: 0xf0, 31: 0x0d},
		Name: "chain",
		IDs:  [][32]byte{{0: 1}, {0: 2}},
		Leaf: Leaf{N: 9, S: "leaf"},
	}
	if _, err := zapenc.Marshal(&Loose{ID: v.ID, Name: v.Name}); err == nil {
		t.Fatal("the reflective encoder carried a fixed array; the refusal is what forces a codec")
	} else if !strings.Contains(err.Error(), "bytes_fixed[32]") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}

	enc, err := v.MarshalZAP()
	if err != nil {
		t.Fatal(err)
	}
	var back Ided
	if err := back.UnmarshalZAP(enc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, back) {
		t.Fatalf("the id did not survive\n sent %+v\n back %+v", v, back)
	}
}

func trunks() []Trunk {
	u, s, b := uint64(42), "pointed", true
	return []Trunk{
		{},
		{
			B: true, I8: -8, I16: -16, I32: -32, I64: -64,
			U8: 8, U16: 16, U32: 32, U64: 64,
			F32: 1.5, F64: -2.25, C: 7,
			Text: "hello", Raw: []byte{1, 2, 3},
			Leaf: Leaf{N: 1, S: "leaf"},
			PtrU: &u, PtrS: &s, PtrB: &b, PtrL: &Leaf{N: 2, S: "ptr"},
			Nums:  []uint32{1, 2, 3},
			Sigs:  []int32{-1, 0, 1},
			Bigs:  []float64{1.5, -2.5},
			Strs:  []string{"a", "bb"},
			Bufs:  [][]byte{{9}, {8, 7}},
			Bits:  []bool{true, false, true},
			Kids:  []Leaf{{N: 3, S: "x"}, {N: 4, S: "y"}},
			Ptrs:  []*Leaf{{N: 5, S: "p"}, {N: 6, S: "q"}},
			Seals: []Sealed{{}, {}},
			PKids: &[]Leaf{{N: 7, S: "pk"}},
			PNums: &[]uint32{11, 12},
		},
		{Text: "only text"},
		{Kids: []Leaf{{}}},
		zeroes(),
		{
			PtrI8: ptr(int8(-8)), PtrI6: ptr(int16(-16)), PtrI3: ptr(int32(-32)), PtrI4: ptr(int64(-64)),
			PtrU1: ptr(uint8(8)), PtrU6: ptr(uint16(16)), PtrU3: ptr(uint32(32)), PtrU: ptr(uint64(64)),
			PtrF3: ptr(float32(1.5)), PtrF6: ptr(-2.25), PtrC: ptr(Count(7)),
			PtrB: ptr(true), PtrS: ptr("set"), PtrR: ptr([]byte{1, 2}),
		},
	}
}

func ptr[T any](v T) *T { return &v }

// zeroes points every optional field at its zero value: present, and zero.
func zeroes() Trunk {
	return Trunk{
		PtrI8: ptr(int8(0)), PtrI6: ptr(int16(0)), PtrI3: ptr(int32(0)), PtrI4: ptr(int64(0)),
		PtrU1: ptr(uint8(0)), PtrU6: ptr(uint16(0)), PtrU3: ptr(uint32(0)), PtrU: ptr(uint64(0)),
		PtrF3: ptr(float32(0)), PtrF6: ptr(0.0), PtrC: ptr(Count(0)),
		PtrB: ptr(false), PtrS: ptr(""), PtrR: ptr([]byte{}), PtrL: &Leaf{},
	}
}

// TestAPointerToZeroIsPresent is the generated codec's half of the optional rule:
// a pointer to zero comes back a pointer to zero, nil comes back nil, and both
// codecs read what either wrote.
func TestAPointerToZeroIsPresent(t *testing.T) {
	for _, want := range []Trunk{{}, zeroes()} {
		enc, err := want.MarshalZAP()
		if err != nil {
			t.Fatal(err)
		}
		var byCodec, byReflection Trunk
		if err := byCodec.UnmarshalZAP(enc); err != nil {
			t.Fatal(err)
		}
		if err := zapenc.Unmarshal(enc, &byReflection); err != nil {
			t.Fatal(err)
		}
		for _, got := range []Trunk{byCodec, byReflection} {
			for i := range reflect.TypeOf(want).NumField() {
				w, g := reflect.ValueOf(want).Field(i), reflect.ValueOf(got).Field(i)
				if w.Kind() != reflect.Pointer || reflect.TypeOf(want).Field(i).Name[:3] != "Ptr" {
					continue
				}
				if w.IsNil() != g.IsNil() {
					t.Errorf("%s: sent nil=%v, read nil=%v", reflect.TypeOf(want).Field(i).Name, w.IsNil(), g.IsNil())
					continue
				}
				if !w.IsNil() && !g.Elem().IsZero() {
					t.Errorf("%s: sent a pointer to zero, read %v", reflect.TypeOf(want).Field(i).Name, g.Elem())
				}
			}
		}
	}
}
