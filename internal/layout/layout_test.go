package layout

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/zap-proto/zip/internal/zapwire"
)

// TestTheLayoutKeepsTheWire is the whole reason the offsets are read from one
// derivation instead of computed a second time. A generated layout goes out one
// pod at a time, so the pod that has it and the pod that has not are speaking
// the same wire — or they are speaking two, and the second one is a silent
// mis-read of every field after the first that moved.
func TestTheLayoutKeepsTheWire(t *testing.T) {
	for _, v := range trunks() {
		was, err := zapwire.Build(&v)
		if err != nil {
			t.Fatalf("reflective: %v", err)
		}
		is, err := v.BuildZAP()
		if err != nil {
			t.Fatalf("generated: %v", err)
		}
		if !bytes.Equal(was, is) {
			t.Fatalf("the generated layout moved the wire\n reflective %d bytes: % x\n generated  %d bytes: % x",
				len(was), was, len(is), is)
		}
	}
}

// TestEitherSideReadsTheOther holds the decode direction: bytes written by one
// builder read back through the other into the same value, both ways round.
func TestEitherSideReadsTheOther(t *testing.T) {
	for _, v := range trunks() {
		enc, err := v.BuildZAP()
		if err != nil {
			t.Fatal(err)
		}
		var byReflection Trunk
		if err := zapwire.Wrap(enc, &byReflection); err != nil {
			t.Fatal(err)
		}
		var byLayout Trunk
		if err := byLayout.WrapZAP(enc); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(byReflection, byLayout) {
			t.Fatalf("the two readers disagree\n reflective %+v\n generated  %+v", byReflection, byLayout)
		}
	}
}

// TestAnIDCrossesOnlyOnAStatedWire is the forcing function stated as a test. The
// reflective builder refuses bytes_fixed[N] — an ids.ID is [32]byte — and the
// same value on a stated wire goes out and comes back whole.
func TestAnIDCrossesOnlyOnAStatedWire(t *testing.T) {
	v := Ided{
		ID:   [32]byte{0: 0xf0, 31: 0x0d},
		Name: "chain",
		IDs:  [][32]byte{{0: 1}, {0: 2}},
		Leaf: Leaf{N: 9, S: "leaf"},
	}
	if _, err := zapwire.Build(&Loose{ID: v.ID, Name: v.Name}); err == nil {
		t.Fatal("the reflective builder carried a fixed array; the refusal is what forces a stated wire")
	} else if !strings.Contains(err.Error(), "bytes_fixed[32]") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}

	enc, err := v.BuildZAP()
	if err != nil {
		t.Fatal(err)
	}
	var back Ided
	if err := back.WrapZAP(enc); err != nil {
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
	}
}
