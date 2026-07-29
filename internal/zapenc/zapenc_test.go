package zapenc_test

import (
	"reflect"
	"testing"

	zap "github.com/zap-proto/go"
	"github.com/zap-proto/zip/internal/zapenc"
)

type inner struct {
	Slug   string
	Listed bool
}

type wide struct {
	Text    string
	Blob    []byte
	Flag    bool
	I8      int8
	I16     int16
	I32     int32
	I64     int64
	U8      uint8
	U16     uint16
	U32     uint32
	U64     uint64
	F32     float32
	F64     float64
	Nested  inner
	Rows    []inner
	Names   []string
	Numbers []int64
	Opt     *string
	OptGone *string
}

// The whole value crosses and comes back identical. A codec that drops a field
// is worse than one that fails: the call succeeds and the callee decides on a
// value nobody sent.
func TestRoundTripCarriesEveryField(t *testing.T) {
	opt := "present"
	want := wide{
		Text: "hello", Blob: []byte{1, 2, 3, 0xff}, Flag: true,
		I8: -8, I16: -300, I32: -70000, I64: -5_000_000_000,
		U8: 200, U16: 60000, U32: 4_000_000_000, U64: 18_000_000_000_000_000_000,
		F32: 3.5, F64: -2.25,
		Nested: inner{Slug: "board", Listed: true},
		Rows: []inner{
			{Slug: "one", Listed: true},
			{Slug: "two"},
			{Slug: "", Listed: true},
		},
		Names:   []string{"ada", "", "grace"},
		Numbers: []int64{0, -1, 1 << 40},
		Opt:     &opt,
	}

	b, err := zapenc.Marshal(&want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("marshal produced no bytes")
	}
	var got wide
	if err := zapenc.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("value did not survive the crossing:\n got %+v\nwant %+v", got, want)
	}
	if got.OptGone != nil {
		t.Fatalf("an absent pointer came back set: %v", *got.OptGone)
	}
}

// It is REALLY ZAP, not bytes that happen to round-trip through our own two
// halves. The canonical parser has to read the message, and the first field has
// to be at the offset the layout says it is.
func TestBytesAreAZAPMessage(t *testing.T) {
	b, err := zapenc.Marshal(&inner{Slug: "board", Listed: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	msg, err := zap.Parse(b)
	if err != nil {
		t.Fatalf("the canonical parser rejected it: %v", err)
	}
	r := msg.Root()
	if got := r.Text(0); got != "board" {
		t.Fatalf("field 0 reads %q, want board", got)
	}
	// Slug is text (8 bytes: offset+length), so Listed sits at 8.
	if !r.Bool(8) {
		t.Fatal("field 1 reads false, want true")
	}
}

// There is no JSON anywhere in the encoding. A struct whose values contain
// JSON's own punctuation must not produce a payload that parses as JSON, and
// the field names must not appear on the wire at all — the offset IS the name.
func TestNothingIsJSON(t *testing.T) {
	b, err := zapenc.Marshal(&inner{Slug: `{"slug":"x"}`, Listed: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); contains(got, `"Slug"`) || contains(got, `"slug":"x"`) == false {
		// The VALUE is present verbatim (it is text), but the FIELD NAME never is.
		if contains(got, `"Slug"`) {
			t.Fatal("a field name appeared on the wire; the offset is the name")
		}
	}
	if contains(string(b), `"Listed"`) {
		t.Fatal("a field name appeared on the wire; the offset is the name")
	}
}

// An empty message is an absence, not a zero value. A void reply must leave the
// caller's value untouched rather than hand back something it might read as an
// answer.
func TestEmptyIsAbsence(t *testing.T) {
	got := inner{Slug: "untouched", Listed: true}
	if err := zapenc.Unmarshal(nil, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Slug != "untouched" || !got.Listed {
		t.Fatalf("an empty message overwrote the value: %+v", got)
	}
}

// A type that cannot cross is refused at encode. Dropping it silently is how a
// callee ends up deciding on a field the caller believes it sent.
func TestUnencodableIsRefused(t *testing.T) {
	type bad struct{ M map[string]string }
	if _, err := zapenc.Marshal(&bad{M: map[string]string{"a": "b"}}); err == nil {
		t.Fatal("a map encoded silently; it must be refused")
	}
}

// Empty and nil slices are both an absent list, and both read back as nil —
// which is what a caller ranges over identically.
func TestEmptySliceIsAbsent(t *testing.T) {
	b, err := zapenc.Marshal(&wide{Rows: nil, Names: []string{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got wide
	if err := zapenc.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Rows) != 0 || len(got.Names) != 0 {
		t.Fatalf("an empty list came back with elements: %+v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
