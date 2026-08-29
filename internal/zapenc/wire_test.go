package zapenc_test

import (
	"bytes"
	"strconv"
	"testing"

	zap "github.com/zap-proto/go"
	"github.com/zap-proto/zip/internal/zapenc"
)

// A type that states its own wire form is never reflected over, and this file
// proves it without instrumenting the encoder.
//
// The lever is a field the reflective derivation CANNOT express. A fixed-size
// array is refused outright — `zapenc: array cannot cross the plane` — so a value
// carrying one either encodes through its own MarshalZAP or it does not encode at
// all. A successful round trip is therefore the proof, and [TestReflectionRefusesTheSameShape]
// is the control that keeps it honest: the identical field list with the methods
// removed still fails, so the pass above is the methods and not the shape.
//
// This is the shape a Lux node reply has. ids.ID is [32]byte, ids.NodeID is
// [20]byte, and both are in nearly every answer the platform chain gives, so
// "the plane cannot carry an id" is not a corner of the type system.

const (
	heightHeightOff = 0
	heightChainOff  = 8  // bytes_fixed[32], inline, aligned to 1
	heightNodeOff   = 40 // bytes_fixed[20], inline
	heightNameOff   = 64 // text pointer, aligned to its own width
	heightSize      = 72
)

// height is what a generated view+codec looks like: offsets are constants, and
// nothing about the layout is computed while a call is in flight.
type height struct {
	Height uint64   `zap:"0"`
	Chain  [32]byte `zap:"8"`
	Node   [20]byte `zap:"40"`
	Name   string   `zap:"64"`
}

func (h *height) MarshalZAP() ([]byte, error) {
	b := zap.NewBuilder(heightSize + len(h.Name) + 32)
	ob := b.StartObject(heightSize)
	ob.SetUint64(heightHeightOff, h.Height)
	ob.SetBytesFixed(heightChainOff, h.Chain[:])
	ob.SetBytesFixed(heightNodeOff, h.Node[:])
	ob.SetText(heightNameOff, h.Name)
	ob.FinishAsRoot()
	return b.Finish(), nil
}

func (h *height) UnmarshalZAP(data []byte) error {
	m, err := zap.Parse(data)
	if err != nil {
		return err
	}
	o := m.Root()
	h.Height = o.Uint64(heightHeightOff)
	copy(h.Chain[:], o.BytesFixed(heightChainOff, 32))
	copy(h.Node[:], o.BytesFixed(heightNodeOff, 20))
	h.Name = o.Text(heightNameOff)
	return nil
}

// heightByFields is height with the methods removed and nothing else changed. It
// is the control: if this ever starts encoding, the proof above has stopped
// meaning anything.
type heightByFields struct {
	Height uint64
	Chain  [32]byte
	Node   [20]byte
	Name   string
}

func TestAWireTypeCarriesAnIDThroughTheReflectiveEncoder(t *testing.T) {
	in := &height{Height: 1 << 60, Name: "P-chain"}
	for i := range in.Chain {
		in.Chain[i] = byte(i + 1)
	}
	for i := range in.Node {
		in.Node[i] = byte(200 - i)
	}

	// Marshal and Unmarshal are the SAME entry points the op-call plane uses.
	// Reaching reflect for this value would fail, so reaching the far side at all
	// is the assertion.
	raw, err := zapenc.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v — the reflective path was taken, and it cannot carry a fixed array", err)
	}
	var out height
	if err := zapenc.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Height != in.Height || out.Name != in.Name {
		t.Fatalf("scalars did not cross: %+v", out)
	}
	if out.Chain != in.Chain {
		t.Fatalf("bytes_fixed[32] did not cross: got %x want %x", out.Chain, in.Chain)
	}
	if out.Node != in.Node {
		t.Fatalf("bytes_fixed[20] did not cross: got %x want %x", out.Node, in.Node)
	}
}

func TestReflectionRefusesTheSameShape(t *testing.T) {
	_, err := zapenc.Marshal(&heightByFields{Name: "P-chain"})
	if err == nil {
		t.Fatal("the reflective encoder accepted a fixed array; the proof above no longer holds")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("array cannot cross the plane")) {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}

// TestAWireTypeIsWhatItSays keeps the generated codec honest about the layout it
// claims. A codec whose offsets drifted from the declaration would still round
// trip against ITSELF, and both ends would agree while disagreeing with every
// peer built from the schema.
func TestAWireTypeIsWhatItSays(t *testing.T) {
	raw, err := zapenc.Marshal(&height{Height: 7})
	if err != nil {
		t.Fatal(err)
	}
	m, err := zap.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Root().Uint64(heightHeightOff); got != 7 {
		t.Fatalf("height is not at offset %d: read %d", heightHeightOff, got)
	}
}

// ---- what it costs -------------------------------------------------------

// wide (zapenc_test.go) is the reflective encoder's own fixture, so the two
// benchmarks below encode a comparable shape by the two paths.

type reply struct {
	Text  string
	Flag  bool
	Count int64
}

type wireReply struct {
	Text  string `zap:"0"`
	Flag  bool   `zap:"8"`
	Count int64  `zap:"16"`
}

const (
	wireReplyTextOff  = 0
	wireReplyFlagOff  = 8
	wireReplyCountOff = 16
	wireReplySize     = 24
)

func (r *wireReply) MarshalZAP() ([]byte, error) {
	b := zap.NewBuilder(wireReplySize + len(r.Text) + 16)
	ob := b.StartObject(wireReplySize)
	ob.SetText(wireReplyTextOff, r.Text)
	ob.SetBool(wireReplyFlagOff, r.Flag)
	ob.SetInt64(wireReplyCountOff, r.Count)
	ob.FinishAsRoot()
	return b.Finish(), nil
}

func (r *wireReply) UnmarshalZAP(data []byte) error {
	m, err := zap.Parse(data)
	if err != nil {
		return err
	}
	o := m.Root()
	r.Text = o.Text(wireReplyTextOff)
	r.Flag = o.Bool(wireReplyFlagOff)
	r.Count = o.Int64(wireReplyCountOff)
	return nil
}

// TestTheTwoPathsWriteTheSameBytes is what makes the migration safe: a type that
// gains a codec must not gain a new wire, or every peer still on the reflective
// path reads a different message.
func TestTheTwoPathsWriteTheSameBytes(t *testing.T) {
	byFields, err := zapenc.Marshal(&reply{Text: "commerce", Flag: true, Count: 42})
	if err != nil {
		t.Fatal(err)
	}
	byCodec, err := zapenc.Marshal(&wireReply{Text: "commerce", Flag: true, Count: 42})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(byFields, byCodec) {
		t.Fatalf("the wire moved:\n reflective %x\n generated  %x", byFields, byCodec)
	}
}

func BenchmarkMarshalByReflection(b *testing.B) {
	v := &reply{Text: "commerce", Flag: true, Count: 42}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := zapenc.Marshal(v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalByCodec(b *testing.B) {
	v := &wireReply{Text: "commerce", Flag: true, Count: 42}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := zapenc.Marshal(v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalByReflection(b *testing.B) {
	raw, err := zapenc.Marshal(&reply{Text: "commerce", Flag: true, Count: 42})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var out reply
		if err := zapenc.Unmarshal(raw, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalByCodec(b *testing.B) {
	raw, err := zapenc.Marshal(&wireReply{Text: "commerce", Flag: true, Count: 42})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var out wireReply
		if err := zapenc.Unmarshal(raw, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// ---- the read that does not copy ------------------------------------------

// A page of rows is the shape the money plane actually answers with — usage,
// transactions, costs — and it is where the two readings stop being comparable.
// The reflective decode MATERIALISES every row whether the caller reads one field
// or none: a slice, an element per row, a string copy per text field. A view
// resolves nothing until it is asked, so its cost is the header check and does not
// move with the row count.

type row struct {
	ID     string
	Amount int64
}

type page struct {
	Rows []row
}

const (
	pageRowsOff  = 0
	pageSize     = 8
	rowIDOff     = 0
	rowAmountOff = 8
	rowSize      = 16
)

// pageView is a generated view: it holds the buffer that arrived and reads a
// field as an offset into it.
type pageView struct{ o zap.Object }

func (p pageView) Rows() zap.List { return p.o.List(pageRowsOff) }

func rowID(l zap.List, i int) string           { return l.ObjectAt(i).Text(rowIDOff) }
func rowAmount(l zap.List, i int) int64        { return l.ObjectAt(i).Int64(rowAmountOff) }
func (p pageView) UnmarshalZAP(b []byte) error { return nil } // unused; see wrapPage

func wrapPage(b []byte) (pageView, error) {
	m, err := zap.Parse(b)
	if err != nil {
		return pageView{}, err
	}
	return pageView{o: m.Root()}, nil
}

func newPage(n int) []byte {
	b := zap.NewBuilder(256 + n*64)
	lb := b.StartList(0)
	for i := range n {
		eb := zap.NewBuilder(64)
		eo := eb.StartObject(rowSize)
		eo.SetText(rowIDOff, "usg_01HZY8Q2K3M4N5P6R7S8T9V0W1")
		eo.SetInt64(rowAmountOff, int64(i))
		eo.FinishAsRoot()
		lb.AddObjectBytes(eb.Finish())
	}
	off := lb.FinishOffset()
	ob := b.StartObject(pageSize)
	ob.SetList(pageRowsOff, off, n)
	ob.FinishAsRoot()
	return b.Finish()
}

// TestTheViewReadsTheBufferThatArrived pins the property the benchmark measures:
// the view answers from the received bytes, with no decoded copy in between.
func TestTheViewReadsTheBufferThatArrived(t *testing.T) {
	raw := newPage(3)
	v, err := wrapPage(raw)
	if err != nil {
		t.Fatal(err)
	}
	l := v.Rows()
	if l.Len() != 3 {
		t.Fatalf("rows: got %d want 3", l.Len())
	}
	if got := rowAmount(l, 2); got != 2 {
		t.Fatalf("row 2 amount: got %d want 2", got)
	}
	// The string is a window onto raw, not a copy of it.
	s := rowID(l, 0)
	if len(s) == 0 || !bytes.Contains(raw, []byte(s)) {
		t.Fatalf("row 0 id did not come from the received buffer: %q", s)
	}
}

// The sizes are what makes the shape visible rather than the ratio: the
// reflective decode is O(rows) because it materialises each one, the view is O(1)
// because it materialises none.
var pageSizes = []int{10, 100, 1000}

func BenchmarkReadPageByReflection(b *testing.B) {
	for _, n := range pageSizes {
		raw := newPage(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var out page
				if err := zapenc.Unmarshal(raw, &out); err != nil {
					b.Fatal(err)
				}
				_ = out.Rows[n-1].Amount
			}
		})
	}
}

func BenchmarkReadPageByView(b *testing.B) {
	for _, n := range pageSizes {
		raw := newPage(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				v, err := wrapPage(raw)
				if err != nil {
					b.Fatal(err)
				}
				_ = rowAmount(v.Rows(), n-1)
			}
		})
	}
}
