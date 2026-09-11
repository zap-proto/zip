package zapwire_test

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/zap-proto/zip/internal/zapwire"
)

// within reports whether p points into b: the read is a window onto the bytes it
// was handed, not a copy of them.
func within(b []byte, p *byte) bool {
	lo := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	at := uintptr(unsafe.Pointer(p))
	return at >= lo && at < lo+uintptr(len(b))
}

// A read is a pointer into the bytes that arrived. Wrap decodes nothing: every
// string and byte slice zap can view comes back as a window onto data, at every
// depth, which is why data is handed over to the value rather than lent.
func TestWrapPointsIntoTheBytesItWasHanded(t *testing.T) {
	opt := "optional"
	b, err := zapwire.Build(&wide{
		Text:   "top-level",
		Blob:   []byte("blob"),
		Nested: inner{Slug: "nested"},
		Rows:   []inner{{Slug: "row-one"}},
		Opt:    &opt,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got wide
	if err := zapwire.Wrap(b, &got); err != nil {
		t.Fatalf("wrap: %v", err)
	}
	for name, p := range map[string]*byte{
		"Text":         unsafe.StringData(got.Text),
		"Blob":         unsafe.SliceData(got.Blob),
		"Nested.Slug":  unsafe.StringData(got.Nested.Slug),
		"Rows[0].Slug": unsafe.StringData(got.Rows[0].Slug),
		"Opt":          unsafe.StringData(*got.Opt),
	} {
		if !within(b, p) {
			t.Errorf("%s was copied out of the message; a read points into it", name)
		}
	}
}

// A byte slice ends at its own length. Capacity running on into the message would
// let an append write over the field after it — a string the program still holds.
func TestABytesFieldEndsAtItsLength(t *testing.T) {
	opt := "after-the-blob"
	want := wide{Blob: []byte("blob"), Opt: &opt}
	b, err := zapwire.Build(&want)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got wide
	if err := zapwire.Wrap(b, &got); err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if cap(got.Blob) != len(got.Blob) {
		t.Fatalf("cap(Blob) = %d, len = %d: an append would run into the next field", cap(got.Blob), len(got.Blob))
	}
	_ = append(got.Blob, "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"...)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("an append to Blob changed the value:\n got %+v\nwant %+v", got, want)
	}
}
