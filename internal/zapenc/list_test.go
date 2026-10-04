package zapenc_test

import (
	"encoding/binary"
	"testing"
	"time"

	zap "github.com/zap-proto/go"
	"github.com/zap-proto/zip/internal/zapenc"
)

type rows struct {
	Rows []inner
}

type names struct {
	Names []string
}

// list writes an object whose one slot is a list holding blob, claiming count.
func list(blob []byte, count int) []byte {
	b := zap.NewBuilder(len(blob) + 64)
	off := b.WriteBytes(blob)
	ob := b.StartObject(8)
	ob.SetList(0, off, count)
	ob.FinishAsRoot()
	return b.Finish()
}

func entries(n int, payload []byte) []byte {
	var blob []byte
	for range n {
		blob = binary.LittleEndian.AppendUint32(blob, uint32(len(payload)))
		blob = append(blob, payload...)
	}
	return blob
}

// A list element that is not a message is an error. It used to be read as a
// null object, and the first field read through it dereferenced nil: one
// malformed call took the whole process down.
func TestAMalformedElementIsAnError(t *testing.T) {
	msg := list(entries(1, []byte("not a message")), 1)
	var got rows
	if err := zapenc.Unmarshal(msg, &got); err == nil {
		t.Fatal("a garbage element decoded without error")
	}
}

// A list is read in one pass. Reading element i by index re-walks the list from
// its start, so a long list took quadratic time — minutes of CPU for a body the
// transport admits.
func TestAListDecodesInOnePass(t *testing.T) {
	const n = 200_000
	msg := list(entries(n, []byte("a")), n)
	start := time.Now()
	var got names
	if err := zapenc.Unmarshal(msg, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Names) != n {
		t.Fatalf("decoded %d of %d", len(got.Names), n)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("%d elements took %s", n, took)
	}
}

// The elements are what the message holds, not what it claims to hold: an entry
// is at least its 4-byte length, so a list in B bytes has at most B/4 of them
// whatever its count says. (A count larger than the message itself is refused by
// zap's own bound and reads as no list.)
func TestAForgedCountYieldsOnlyRealElements(t *testing.T) {
	const claim = 32
	msg := list(entries(2, []byte("ab")), claim)
	if len(msg) <= claim {
		t.Fatalf("message of %d bytes cannot carry a claim of %d past zap's bound", len(msg), claim)
	}
	var got names
	if err := zapenc.Unmarshal(msg, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Names) > len(msg)/4 {
		t.Fatalf("decoded %d elements from %d bytes, claimed %d", len(got.Names), len(msg), claim)
	}
}
