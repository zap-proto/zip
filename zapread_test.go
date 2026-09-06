package zip

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// The four method shapes a ZAP interface can declare, over two structs that are
// distinguishable only by name — which is the case a run-time type system gets
// wrong, because reflect interns two identical shapes into one type.
const echoZAP = `package echo

struct Ping {
    Seq u64 @0
}

struct Pong {
    Seq u64 @0
}

interface Echo {
    ping(req: Ping) returns (resp: Pong)
    notify(req: Ping)
    health() returns (resp: Pong)
    shutdown()
}
`

func readOne(t *testing.T, name, src string) *App {
	t.Helper()
	apps, err := ReadZAP(name, []byte(src))
	if err != nil {
		t.Fatalf("ReadZAP: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("want 1 app, got %d", len(apps))
	}
	return apps[0]
}

// A method becomes an op addressed at its own name, and the op's id is the
// method's — not one derived from the address, which is what lets a schema read
// here and projected back name the same methods.
func TestReadZAP_MethodsBecomeOps(t *testing.T) {
	app := readOne(t, "echo.zap", echoZAP)
	if app.Name() != "Echo" {
		t.Errorf("app name = %q, want Echo (the interface's)", app.Name())
	}

	got := map[string]*registeredOp{}
	for _, op := range app.Registry() {
		got[opName(op)] = op
	}
	for _, want := range []string{"ping", "notify", "health", "shutdown"} {
		op, ok := got[want]
		if !ok {
			t.Fatalf("no op %q; got %v", want, keysOf(got))
		}
		if op.Method != "POST" || op.Path != "/"+want {
			t.Errorf("%s is %s %s, want POST /%s", want, op.Method, op.Path, want)
		}
	}
	if len(got) != 4 {
		t.Errorf("got %d ops, want 4: %v", len(got), keysOf(got))
	}

	// The four shapes: a direction that carries nothing is a struct with no
	// fields, which every projection already reads as an absent payload.
	for _, c := range []struct{ op, in, out string }{
		{"ping", "Ping", "Pong"},
		{"notify", "Ping", ""},
		{"health", "", "Pong"},
		{"shutdown", "", ""},
	} {
		if n := typeName(got[c.op].InType); n != c.in {
			t.Errorf("%s takes %q, want %q", c.op, n, c.in)
		}
		if n := typeName(got[c.op].OutType); n != c.out {
			t.Errorf("%s answers %q, want %q", c.op, n, c.out)
		}
	}
}

// Two declarations of the same shape are two types. reflect interns structurally
// identical types, so Ping and Pong would be one — and a method declared to
// answer Pong would answer Ping in every SDK generated from this schema.
func TestReadZAP_SameShapeDifferentNamesStayApart(t *testing.T) {
	app := readOne(t, "echo.zap", echoZAP)
	var ping, pong reflect.Type
	for _, op := range app.Registry() {
		if opName(op) == "ping" {
			ping, pong = op.InType, op.OutType
		}
	}
	if ping == pong {
		t.Fatalf("Ping and Pong collapsed into one type: %s", ping)
	}
	if typeName(ping) != "Ping" || typeName(pong) != "Pong" {
		t.Fatalf("names lost: %q and %q", typeName(ping), typeName(pong))
	}
}

// The JSON edge keeps the schema's own spelling, so a field the schema wrote in
// lower case crosses under the name it was written with even though the Go field
// has to be exported to be reachable by reflection at all.
func TestReadZAP_FieldKeepsItsDeclaredName(t *testing.T) {
	app := readOne(t, "s.zap", `package s
struct In {
    seq u64 @0
}
interface S {
    go_(req: In)
}
`)
	in := app.Registry()[0].InType
	f := in.Field(0)
	if f.Name != "Seq" {
		t.Errorf("Go field = %q, want Seq (exported so reflection can read it)", f.Name)
	}
	if got := f.Tag.Get("json"); got != "seq" {
		t.Errorf("json name = %q, want seq (the schema's own spelling)", got)
	}
}

// Every schema type has a Go type, and the layout the schema states is the one
// this process derives.
func TestReadZAP_TypesAndOffsets(t *testing.T) {
	app := readOne(t, "wide.zap", `package wide
type id32 = bytes_fixed[32]

struct Wide {
    Flag  bool      @0
    Small u8        @1
    Mid   i32       @4
    Big   u64       @8
    Name  text      @16
    Blob  bytes     @24
    Ids   list<u64> @32
    Id    id32      @40
    Ratio f64       @72
}

interface W {
    take(req: Wide)
}
`)
	in := app.Registry()[0].InType
	want := []struct {
		name string
		kind string
	}{
		{"Flag", "bool"}, {"Small", "uint8"}, {"Mid", "int32"}, {"Big", "uint64"},
		{"Name", "string"}, {"Blob", "[]uint8"}, {"Ids", "[]uint64"},
		{"Id", "[32]uint8"}, {"Ratio", "float64"},
	}
	if in.NumField() != len(want) {
		t.Fatalf("got %d fields, want %d", in.NumField(), len(want))
	}
	for i, w := range want {
		f := in.Field(i)
		if f.Name != w.name || f.Type.String() != w.kind {
			t.Errorf("field %d is %s %s, want %s %s", i, f.Name, f.Type, w.name, w.kind)
		}
	}
	// The offsets the schema stated are the offsets LayoutOf derives — checked
	// by ReadZAP itself, and restated here so a change to either is visible.
	shape, err := LayoutOf(in)
	if err != nil {
		t.Fatalf("LayoutOf: %v", err)
	}
	for i, off := range []int{0, 1, 4, 8, 16, 24, 32, 40, 72} {
		if shape.Slots[i].Offset != off {
			t.Errorf("%s lays out at @%d, schema says @%d", shape.Slots[i].Name, shape.Slots[i].Offset, off)
		}
	}
}

// A .zap file states an offset and LayoutOf derives one. Where they differ the
// schema describes a wire this process does not speak, so it is refused rather
// than silently re-laid-out — and the refusal carries both numbers, because the
// author needs to know which one moved.
//
// The case is not contrived: the IDL's own offset assignment PACKS, and this
// layout ALIGNS each slot to its own width, so any i32 followed by a wider field
// diverges.
func TestReadZAP_RefusesAnOffsetItCannotKeep(t *testing.T) {
	_, err := ReadZAP("packed.zap", []byte(`package p
struct Packed {
    User  text @0
    Count i32  @8
    Rest  u64  @12
}
interface P {
    take(req: Packed)
}
`))
	if err == nil {
		t.Fatal("a packed offset was accepted; an SDK from this schema would encode at the wrong bytes")
	}
	for _, want := range []string{"Packed.Rest", "@12", "@16"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %s: %v", want, err)
		}
	}
}

// Every field that disagrees is named. A work list that stopped at the first
// understates the change by however many the schema has.
func TestReadZAP_NamesEveryOffsetThatDisagrees(t *testing.T) {
	_, err := ReadZAP("packed.zap", []byte(`package p
struct A {
    N i32 @0
    X u64 @4
}
struct B {
    N i32 @0
    Y text @4
}
interface P {
    a(req: A)
    b(req: B)
}
`))
	if err == nil {
		t.Fatal("accepted two packed structs")
	}
	if !strings.Contains(err.Error(), "A.X") || !strings.Contains(err.Error(), "B.Y") {
		t.Errorf("refusal names only some of the disagreements: %v", err)
	}
}

// A schema that declares data and no service is a good schema with nothing to
// project. Answering with an empty registry instead is the failure this whole
// path exists to end.
func TestReadZAP_RefusesASchemaWithNoInterface(t *testing.T) {
	_, err := ReadZAP("data.zap", []byte("package d\nstruct D {\n    X u64 @0\n}\n"))
	if err == nil || !strings.Contains(err.Error(), "no interface") {
		t.Fatalf("want a refusal naming the absent interface, got %v", err)
	}
}

// One app per interface, which is exactly what ZAPSchema takes on the way out.
func TestReadZAP_OneAppPerInterface(t *testing.T) {
	apps, err := ReadZAP("two.zap", []byte(`package t
struct X { N u64 @0 }
interface A { one(req: X) }
interface B { two(req: X) }
`))
	if err != nil {
		t.Fatalf("ReadZAP: %v", err)
	}
	if len(apps) != 2 || apps[0].Name() != "A" || apps[1].Name() != "B" {
		t.Fatalf("want apps A and B, got %d", len(apps))
	}
	if n := len(apps[0].Registry()); n != 1 {
		t.Errorf("A has %d ops, want 1", n)
	}
}

func TestReadZAP_Refusals(t *testing.T) {
	cases := map[string]struct{ src, says string }{
		"a struct that is not declared": {
			src:  "package p\ninterface P {\n    f(req: Nope)\n}\n",
			says: "not declared",
		},
		"a struct that reaches itself": {
			src:  "package p\nstruct S {\n    Me S @0\n}\ninterface P {\n    f(req: S)\n}\n",
			says: "contains itself",
		},
		"two methods of one name": {
			src:  "package p\nstruct X { N u64 @0 }\ninterface P {\n    f(req: X)\n    f(req: X)\n}\n",
			says: "declared twice",
		},
		"two fields that are one Go name": {
			src:  "package p\nstruct X {\n    my_id u64 @0\n    myId  u64 @8\n}\ninterface P {\n    f(req: X)\n}\n",
			says: "MyId",
		},
		"a list of lists": {
			src:  "package p\nstruct X {\n    N list<list<u64>> @0\n}\ninterface P {\n    f(req: X)\n}\n",
			says: "slice of slices",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadZAP("t.zap", []byte(c.src)); err == nil {
				t.Fatal("accepted")
			} else if !strings.Contains(err.Error(), c.says) {
				t.Errorf("refusal does not say %q: %v", c.says, err)
			}
		})
	}
}

// A schema states what an op takes and answers, never what it does. Every way
// into a declared op refuses the same way, so nothing can reach a handler that
// was never written.
func TestReadZAP_ADeclaredOpDoesNotRun(t *testing.T) {
	app := readOne(t, "echo.zap", echoZAP)
	var op *registeredOp
	for _, o := range app.Registry() {
		if opName(o) == "ping" {
			op = o
		}
	}
	if _, err := op.direct(context.Background(), nil); err == nil {
		t.Error("a declared op ran")
	} else if !strings.Contains(err.Error(), "no implementation") {
		t.Errorf("refusal does not say why: %v", err)
	}
	if _, err := op.invoke(context.Background(), nil, nil, nil, nil, nil); err == nil {
		t.Error("a declared op ran over a transport")
	}
}

// ---- the loop --------------------------------------------------------------

type rtHeight struct {
	Height uint64
	Name   string
}

type rtRange struct {
	From   uint64
	To     uint64
	Blocks []uint64
	Memo   []byte
}

// The inverse is exact. An app projected to a schema, read back and projected
// again is the same text: same structs, same offsets, same interface, same
// method names. Anything this file decided that the forward projection did not
// would show up here as a difference.
func TestReadZAP_ProjectsBackToTheSameSchema(t *testing.T) {
	a := New(Config{AppName: "probe"})
	Get(a, "/v1/height", nop[rtHeight, rtHeight])
	Post(a, "/v1/range", nop[rtRange, rtRange])
	Delete(a, "/v1/range", nop[rtRange, rtRange])

	first := ZAPSchema("probe", a).String()

	apps, err := ReadZAP("probe.zap", []byte(first))
	if err != nil {
		t.Fatalf("reading back what this package just wrote: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("want 1 app, got %d", len(apps))
	}
	second := ZAPSchema("probe", apps[0]).String()

	if first != second {
		t.Errorf("the loop does not close:\n--- written ---\n%s\n--- read and written again ---\n%s", first, second)
	}
	// And the structs really are named — a loop that closed on two anonymous
	// spellings would prove nothing.
	if !strings.Contains(second, "struct rtHeight {") || !strings.Contains(second, "struct rtRange {") {
		t.Errorf("the round trip lost the struct names:\n%s", second)
	}
}

// The SDK is the point: a schema in, a client with one method per declared
// method and one struct per declared struct out.
func TestReadZAP_ProjectsAnSDK(t *testing.T) {
	app := readOne(t, "echo.zap", echoZAP)
	res, err := app.RustSDK("demo")
	if err != nil {
		t.Fatalf("RustSDK: %v", err)
	}
	src := string(res.Source)
	for _, want := range []string{
		"pub struct Ping {", "pub struct Pong {",
		"pub async fn ping(&self, input: &Ping, ) -> Result<Pong, Error>",
		"pub async fn notify(&self, input: &Ping) -> Result<(), Error>",
		"pub async fn health(&self, ) -> Result<Pong, Error>",
		"pub async fn shutdown(&self, ) -> Result<(), Error>",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the SDK is missing %q:\n%s", want, src)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
