package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// ---------------------------------------------------------------------------
// The fixture these tests share.
//
// Declared at package scope, because reflect gives a type declared inside a
// function no package path — and [zip.Codecs] groups by the package that owns
// the type, which is the question here: the SDK restates these types in a
// package of its own, and the codec it writes beside them has to be the codec
// zip writes here.
// ---------------------------------------------------------------------------

// TxID is an id — [32]byte, the bytes_fixed[32] of a .zap schema, and the exact
// value the reflective encoder refuses.
type TxID [32]byte

// Leaf is a value nested inside another, so a codec reaches it BY ITS METHOD.
// It holds no id of its own, which is the point: it is coded because the tree
// above it is.
type Leaf struct {
	N uint32 `json:"n"`
	S string `json:"s"`
}

// Tx is the shape node's id-carrying replies actually have. The id sits FIRST
// and is 32 bytes inline aligned to 1, so a second derivation that packed it or
// aligned it to its width moves every field after it.
type Tx struct {
	ID      [32]byte   `json:"id"`
	Height  uint64     `json:"height"`
	Chain   string     `json:"chain"`
	Parents [][32]byte `json:"parents"`
	Leaf    Leaf       `json:"leaf"`
	Leaves  []Leaf     `json:"leaves"`
	Weights []uint32   `json:"weights"`
}

// Ided spells the same kind of wire with NAMED types, which the SDK cannot
// name: it restates TxID as the [32]uint8 it is. So the two codecs spell the
// field differently and must still write the same bytes — that divergence is
// the one this fixture exists for.
type Ided struct {
	ID   TxID   `json:"id"`
	Ref  *TxID  `json:"ref"`
	IDs  []TxID `json:"ids"`
	Name string `json:"name"`
}

// TestSDK_TheCodecIsTheOneZipEmits is the wire claim stated as source. A Go
// client and the Rust and C++ clients speak to the same service, so what the
// SDK writes for an id has to be what zip writes for it — not something that
// also happens to work.
//
// Where the SDK keeps the type's own name and its own spelling, "the same
// codec" is literal: the section is byte-identical, because it is the same
// emitter reading the same [LayoutOf] offsets. Anything less than identical
// here is a second wire.
func TestSDK_TheCodecIsTheOneZipEmits(t *testing.T) {
	app := zip.New(zip.Config{AppName: "p", DisableStartupMessage: true})
	zip.Post(app, "/v1/tx", func(_ context.Context, in *Tx) (*Tx, error) { return in, nil },
		zip.WithOperationID("get_tx"))

	sdk, err := app.SDK("p")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	native, err := zip.Codecs(reflect.TypeOf(Tx{}))
	if err != nil {
		t.Fatalf("Codecs: %v", err)
	}
	if len(native) != 1 {
		t.Fatalf("got %d packages, want 1", len(native))
	}
	for _, name := range native[0].Types {
		want := section(t, string(native[0].Source), name)
		if !strings.Contains(string(sdk.Source), want) {
			t.Errorf("the SDK's codec for %s is not the one zip emits.\n--- zip ---\n%s\n--- sdk ---\n%s",
				name, want, sdk.Source)
		}
	}
}

// section is one type's codec, cut out of an emitted file by the banner the
// emitter writes above it.
func section(t *testing.T, src, name string) string {
	t.Helper()
	head := "// ---- " + name + " "
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("no %s section in the emitted file:\n%s", name, src)
	}
	rest := src[i+len(head):]
	if j := strings.Index(rest, "\n// ---- "); j >= 0 {
		return src[i : i+len(head)+j]
	}
	return strings.TrimRight(src[i:], "\n")
}

// TestSDK_OnlyTheTreesThatNeedOneStateTheirWire is the line the emission draws,
// and why it is drawn there.
//
// A codec is not free of consequence: a parent encoded reflectively reflects
// over its children too, so a nested codec is reached by the parent's METHOD
// and never by the reflective walk. That makes the set closed upward AND
// downward — and it also means every type given one is a type whose wire moved
// from derived to stated. Ops that already crossed do not need that, and do not
// get it: the change is confined to the trees an id made unreachable.
func TestSDK_OnlyTheTreesThatNeedOneStateTheirWire(t *testing.T) {
	// wrap holds the id two levels down, so nothing about wrap is fixed and
	// wrap still has to state its wire — that is the upward half.
	type wrap struct {
		Name string `json:"name"`
		Tx   Tx     `json:"tx"`
	}
	// plain is a whole tree with no id in it, and keeps the wire it had.
	type plainLeaf struct {
		N uint64 `json:"n"`
	}
	type plain struct {
		A uint64      `json:"a"`
		B string      `json:"b"`
		L []plainLeaf `json:"l"`
	}
	app := zip.New(zip.Config{AppName: "p", DisableStartupMessage: true})
	zip.Post(app, "/v1/wrap", func(_ context.Context, in *wrap) (*wrap, error) { return in, nil },
		zip.WithOperationID("get_wrap"))
	zip.Post(app, "/v1/plain", func(_ context.Context, in *plain) (*plain, error) { return in, nil },
		zip.WithOperationID("get_plain"))

	sdk, err := app.SDK("p")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if len(sdk.Gaps) != 0 {
		t.Fatalf("gaps: %+v", sdk.Gaps)
	}
	src := string(sdk.Source)
	// Upward from the id, and downward from what the id forced.
	for _, want := range []string{"Wrap", "Tx", "Leaf"} {
		if !strings.Contains(src, "func (x *"+want+") MarshalZAP()") {
			t.Errorf("%s holds an id in its tree and does not state its wire:\n%s", want, src)
		}
	}
	// And nowhere else. A tree with no id keeps the derived wire it had, which
	// is what makes this change touch only the ops that could not cross.
	for _, no := range []string{"Plain", "PlainLeaf"} {
		if strings.Contains(src, "func (x *"+no+") MarshalZAP()") {
			t.Errorf("%s has no id anywhere in it and was re-encoded anyway:\n%s", no, src)
		}
	}
}

// TestSDK_TheTwoCodecsWriteTheSameBytes is the claim run rather than read.
//
// It builds a module holding both: the service's types with the codec zip emits
// for them, and the generated SDK with the codec the SDK emits for its
// restatement of them. The same value is put in both — through the json tags
// the SDK copies verbatim, so nothing is filled in twice by hand — and the two
// MarshalZAP calls must agree byte for byte, each side must read the other's
// bytes back to the value it started from, and the whole thing must survive a
// real ZAP call over a real socket.
//
// A Go client that agreed with nobody would still round-trip against itself.
// This is the test that a Rust or C++ client, which reaches these ops already,
// is talking to the same wire.
func TestSDK_TheTwoCodecsWriteTheSameBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a module with the go toolchain")
	}
	app := zip.New(zip.Config{AppName: "p", DisableStartupMessage: true})
	zip.Post(app, "/v1/tx", func(_ context.Context, in *Tx) (*Tx, error) { return in, nil },
		zip.WithOperationID("get_tx"))
	zip.Post(app, "/v1/ided", func(_ context.Context, in *Ided) (*Ided, error) { return in, nil },
		zip.WithOperationID("get_ided"))

	sdk, err := app.SDK("p")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if len(sdk.Gaps) != 0 {
		t.Fatalf("gaps: %+v", sdk.Gaps)
	}
	native, err := zip.Codecs(reflect.TypeOf(Tx{}), reflect.TypeOf(Ided{}))
	if err != nil {
		t.Fatalf("Codecs: %v", err)
	}
	// The emitter names the package the TYPES live in, and in the probe they
	// live in svc. That one line is the only edit; the codec below it is the
	// bytes zip wrote.
	gen := bytes.Replace(native[0].Source,
		[]byte("\npackage "+native[0].Package+"\n"), []byte("\npackage svc\n"), 1)
	if bytes.Equal(gen, native[0].Source) {
		t.Fatalf("the package clause was not where it was expected:\n%s", native[0].Source)
	}

	dir := t.TempDir()
	zipDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, body []byte) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	directive, zapv := "go 1.26.5", "v1.3.0"
	for _, line := range strings.Split(string(mod), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "go ") {
			directive = line
		}
		if f := strings.Fields(line); len(f) == 2 && f[0] == "github.com/zap-proto/go" {
			zapv = f[1]
		}
	}
	write("go.mod", []byte("module sdkprobe\n\n"+directive+
		"\n\nrequire (\n\tgithub.com/zap-proto/go "+zapv+
		"\n\tgithub.com/zap-proto/zip v1.36.16\n)\n\nreplace github.com/zap-proto/zip => "+zipDir+"\n"))
	write("go.sum", sum)
	write("p/client.go", sdk.Source)
	write("svc/types.go", []byte(probeTypes))
	write("svc/zap_gen.go", gen)
	write("main.go", []byte(strings.Replace(probeMain, "@DOCS@", probeDocs(t), 1)))

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "missing go.sum entry") || strings.Contains(string(out), "cannot find module") {
			t.Skipf("module cache does not hold zip's dependencies: %s", out)
		}
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "SAME WIRE OK") {
		t.Fatalf("the two codecs are not the same wire:\n%s", out)
	}
}

// probeDocs is the values the probe puts through both codecs, as JSON. They are
// filled HERE, off the declared type, so the probe carries no second copy of
// them to drift — and the SDK copies the json tags verbatim, so one document
// lands in both shapes identically.
func probeDocs(t *testing.T) string {
	t.Helper()
	r := rand.New(rand.NewSource(1))
	random := Tx{Height: r.Uint64(), Chain: "chain/ünïcode", Weights: []uint32{r.Uint32(), 0, r.Uint32()}}
	r.Read(random.ID[:])
	for range 3 {
		var id [32]byte
		r.Read(id[:])
		random.Parents = append(random.Parents, id)
	}
	random.Leaf = Leaf{N: r.Uint32(), S: "leaf"}
	random.Leaves = []Leaf{{N: 0, S: ""}, {N: r.Uint32(), S: "ÿ"}}

	full := Tx{Height: ^uint64(0), Chain: "ÿ", Weights: []uint32{^uint32(0)}}
	for i := range full.ID {
		full.ID[i] = 0xFF
	}
	full.Parents = [][32]byte{full.ID}
	full.Leaf = Leaf{N: ^uint32(0), S: "ÿ"}
	full.Leaves = []Leaf{full.Leaf}

	rid := Ided{Name: "ided"}
	r.Read(rid.ID[:])
	ref := TxID(full.ID)
	rid.Ref, rid.IDs = &ref, []TxID{TxID(full.ID), TxID(random.ID)}

	var b strings.Builder
	b.WriteString("var txDocs = []string{\n")
	for _, v := range []Tx{{}, full, random} {
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "\t%q,\n", j)
	}
	b.WriteString("}\n\nvar idedDocs = []string{\n")
	// The zero Ided has a nil *TxID: an inline slot has no null, so absence
	// comes back as all-zero, which is what the codec reads as nil.
	for _, v := range []Ided{{}, {ID: TxID(full.ID), Ref: &ref, IDs: rid.IDs, Name: "ÿ"}, rid} {
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "\t%q,\n", j)
	}
	b.WriteString("}\n")
	return b.String()
}

// probeTypes restates the service's types in the probe. If it drifted from the
// declaration the codec beside it was generated from, that codec would name a
// field this struct does not have, and the module would not build.
const probeTypes = `package svc

type TxID [32]byte

type Leaf struct {
	N uint32 ` + "`json:\"n\"`" + `
	S string ` + "`json:\"s\"`" + `
}

type Tx struct {
	ID      [32]byte   ` + "`json:\"id\"`" + `
	Height  uint64     ` + "`json:\"height\"`" + `
	Chain   string     ` + "`json:\"chain\"`" + `
	Parents [][32]byte ` + "`json:\"parents\"`" + `
	Leaf    Leaf       ` + "`json:\"leaf\"`" + `
	Leaves  []Leaf     ` + "`json:\"leaves\"`" + `
	Weights []uint32   ` + "`json:\"weights\"`" + `
}

type Ided struct {
	ID   TxID   ` + "`json:\"id\"`" + `
	Ref  *TxID  ` + "`json:\"ref\"`" + `
	IDs  []TxID ` + "`json:\"ids\"`" + `
	Name string ` + "`json:\"name\"`" + `
}
`

const probeMain = `package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/zap-proto/zip"
	"sdkprobe/p"
	svc "sdkprobe/svc"
)

@DOCS@

type wire interface {
	MarshalZAP() ([]byte, error)
	UnmarshalZAP([]byte) error
}

// same puts one document into both shapes, encodes each with its own codec, and
// requires the bytes and both cross-reads to agree.
func same(what, doc string, a, b wire) {
	if err := json.Unmarshal([]byte(doc), a); err != nil {
		fail("%s: service side: %v", what, err)
	}
	if err := json.Unmarshal([]byte(doc), b); err != nil {
		fail("%s: sdk side: %v", what, err)
	}
	x, err := a.MarshalZAP()
	if err != nil {
		fail("%s: service codec: %v", what, err)
	}
	y, err := b.MarshalZAP()
	if err != nil {
		fail("%s: sdk codec: %v", what, err)
	}
	if !bytes.Equal(x, y) {
		fail("%s: two wires\n zip %d bytes: %% x\n sdk %d bytes: %% x", what, len(x), len(y))
	}
	// Each side reads what the other wrote back to the value it started from.
	back := reflect.New(reflect.TypeOf(a).Elem()).Interface().(wire)
	if err := back.UnmarshalZAP(y); err != nil {
		fail("%s: service reading the sdk's bytes: %v", what, err)
	}
	if !reflect.DeepEqual(a, back) {
		fail("%s: the service read the sdk's bytes as something else\n was %+v\n got %+v", what, a, back)
	}
	other := reflect.New(reflect.TypeOf(b).Elem()).Interface().(wire)
	if err := other.UnmarshalZAP(x); err != nil {
		fail("%s: sdk reading the service's bytes: %v", what, err)
	}
	if !reflect.DeepEqual(b, other) {
		fail("%s: the sdk read the service's bytes as something else\n was %+v\n got %+v", what, b, other)
	}
}

func main() {
	for i, doc := range txDocs {
		same(fmt.Sprintf("tx[%d]", i), doc, &svc.Tx{}, &p.Tx{})
	}
	for i, doc := range idedDocs {
		same(fmt.Sprintf("ided[%d]", i), doc, &svc.Ided{}, &p.Ided{})
	}

	// And over a real socket, against a service holding the declared types.
	app := zip.New(zip.Config{AppName: "p", DisableStartupMessage: true})
	zip.Post(app, "/v1/tx", func(_ context.Context, in *svc.Tx) (*svc.Tx, error) { return in, nil },
		zip.WithOperationID("get_tx"))
	zip.Post(app, "/v1/ided", func(_ context.Context, in *svc.Ided) (*svc.Ided, error) { return in, nil },
		zip.WithOperationID("get_ided"))

	sock := filepath.Join(os.TempDir(), fmt.Sprintf("sdkwire-%d.sock", os.Getpid()))
	defer os.Remove(sock)
	if _, err := zip.Serve(app, sock); err != nil {
		fail("serve: %v", err)
	}
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var want svc.Tx
	if err := json.Unmarshal([]byte(txDocs[len(txDocs)-1]), &want); err != nil {
		fail("%v", err)
	}
	var sent p.Tx
	if err := json.Unmarshal([]byte(txDocs[len(txDocs)-1]), &sent); err != nil {
		fail("%v", err)
	}

	c, err := p.Dial(sock)
	if err != nil {
		fail("dial: %v", err)
	}
	defer c.Close()

	var got *p.Tx
	for i := 0; i < 100; i++ {
		if got, err = c.GetTx(context.Background(), &sent); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		fail("GetTx: %v", err)
	}
	if !reflect.DeepEqual(*got, sent) {
		fail("the value did not survive the call\n sent %+v\n got  %+v", sent, *got)
	}
	// The id itself, read off the far end rather than inferred from equality.
	if got.ID != want.ID {
		fail("the id came back different: %x vs %x", got.ID, want.ID)
	}
	fmt.Println("SAME WIRE OK")
}

func fail(format string, a ...any) {
	fmt.Printf(format+"\n", a...)
	os.Exit(1)
}
`

// TestSDK_AValueWithNoSlotsIsStillWrittenInline holds the one exception to
// "reached by its method". A type whose fields are all unexported — a time.Time,
// a netip.AddrPort — has no slots, so it crosses as a complete and EMPTY object
// that its parent writes inline. There is nothing to state and no method to
// reach for, and [Codecs] answers the same way. A codec here that called
// MarshalZAP on it would be a method that does not exist.
func TestSDK_AValueWithNoSlotsIsStillWrittenInline(t *testing.T) {
	type stamped struct {
		ID   [32]byte  `json:"id"`
		When time.Time `json:"when"`
		Name string    `json:"name"`
	}
	app := zip.New(zip.Config{AppName: "p", DisableStartupMessage: true})
	zip.Post(app, "/v1/stamped", func(_ context.Context, in *stamped) (*stamped, error) { return in, nil },
		zip.WithOperationID("get_stamped"))

	sdk, err := app.SDK("p")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if len(sdk.Gaps) != 0 {
		t.Fatalf("gaps: %+v", sdk.Gaps)
	}
	src := string(sdk.Source)
	if !strings.Contains(src, "func (x *Stamped) MarshalZAP()") {
		t.Fatalf("the type holding the id does not state its wire:\n%s", src)
	}
	if strings.Contains(src, "func (x *Time) MarshalZAP()") {
		t.Errorf("a value with no slots was given a codec:\n%s", src)
	}
	if strings.Contains(src, "x.When.MarshalZAP()") {
		t.Errorf("the parent reaches for a method the value does not have:\n%s", src)
	}
	// What zip writes for one, written the same way here: a complete and empty
	// object, laid down inline.
	if !strings.Contains(src, "eb.StartObject(0).FinishAsRoot()") {
		t.Errorf("the empty value is not written inline:\n%s", src)
	}
}
