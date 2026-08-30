package zip

import (
	"reflect"
	"strings"
	"testing"
)

// An OPTIONAL id is a pointer to a fixed array, and the two directions of the
// generated codec disagreed about it: the writer guarded the nil and the reader
// did not, so `copy(x.F[:], …)` dereferenced a nil pointer on a value that had
// just been allocated. Every read of a type carrying one panicked at the far
// end of the plane — measured on the P-Chain's L1 validator, whose validationID
// is exactly this shape.
type optionalID struct {
	ID   *[32]byte `json:"id"`
	Name string    `json:"name"`
}

func TestAnOptionalIDIsReadBack(t *testing.T) {
	written, err := Codecs(reflect.TypeOf(optionalID{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 {
		t.Fatalf("got %d files, want 1", len(written))
	}
	src := string(written[0].Source)

	// The reader must not write through the pointer before it exists.
	if strings.Contains(src, "copy(x.ID[:]") {
		t.Errorf("the reader dereferences a nil pointer:\n%s", src)
	}
	// It allocates, then keeps the pointer only where something arrived — an
	// inline slot has no null, so all-zero is what an absent pointer looks
	// like, the same rule the pointer scalars already read by.
	for _, want := range []string{
		"var v [32]uint8",
		"copy(v[:], raw)",
		"x.ID = &v",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the reader is missing %q:\n%s", want, src)
		}
	}
}
