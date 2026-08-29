package zip

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip/internal/zapenc"
)

// ---- the types the cases below are built from ------------------------------

type schHeight struct {
	Height uint64
}

type schValidator struct {
	NodeID [20]byte
	Weight uint64
}

type schValidators struct {
	Validators []schValidator
	Total      uint64
}

// schNarrow is the alignment case: a byte, then a word. The word begins at 8,
// not at 1 — the layout aligns a field to its own width, and a schema that
// packed them would state offsets nothing writes.
type schNarrow struct {
	Flag bool
	Big  uint64
}

type schEmpty struct{}

type schMapped struct {
	Labels map[string]string
	Name   string
}

type schAny struct {
	Value any
}

type schRaw struct {
	Body json.RawMessage
}

type schHolder struct {
	At   time.Time
	Name string
}

type schNested struct {
	Inner schHeight
	Name  string
}

type schEmbedded struct {
	schHeight
	Extra string
}

type schWide struct {
	Grid [4]uint32
}

func nop[In, Out any](context.Context, *In) (*Out, error) { return nil, nil }

func schemaOfApp(t *testing.T, build func(*App)) *Schema {
	t.Helper()
	a := New(Config{AppName: "probe"})
	build(a)
	return ZAPSchema("probe", a)
}

// ---- the shape -------------------------------------------------------------

// The whole artifact, in one golden: struct declarations carrying the layout's
// own byte offsets, then one interface whose method names are the ids every
// other projection uses.
func TestZAPSchema_RendersTheIDL(t *testing.T) {
	s := schemaOfApp(t, func(a *App) {
		Get(a, "/v1/height", nop[schEmpty, schHeight])
		Get(a, "/v1/validators", nop[schEmpty, schValidators])
	})

	const want = `# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package probe

struct schHeight {
    Height u64 @0
}

struct schValidators {
    Validators list<bytes> @0
    Total      u64         @8
}

interface probe {
    get_height() returns (rep: schHeight)
    get_validators() returns (rep: schValidators)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   schValidators.Validators  zip.schValidator (list element)
`
	if got := s.String(); got != want {
		t.Fatalf("schema mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// THE OFFSETS ARE THE LAYOUT'S, NOT THIS PACKAGE'S. Every field of every struct
// must sit where LayoutOf says: a schema that derived its own would describe a
// wire nobody speaks, and the two would agree everywhere except the places
// reasoning gets wrong — which is what makes this worth asserting.
func TestZAPSchema_OffsetsAreTheLayouts(t *testing.T) {
	s := schemaOfApp(t, func(a *App) {
		Get(a, "/v1/n", nop[schEmpty, schNarrow])
		Get(a, "/v1/v", nop[schEmpty, schValidator])
		Get(a, "/v1/x", nop[schEmpty, schNested])
	})
	if len(s.Structs) == 0 {
		t.Fatal("nothing to compare")
	}
	for _, st := range s.Structs {
		v, ok := sample(st.From)
		if !ok {
			t.Fatalf("no sample for %s; the case must compare every struct it emits", st.From)
		}
		shape, err := LayoutOf(reflect.TypeOf(v))
		if err != nil {
			t.Fatalf("LayoutOf(%s): %v", st.From, err)
		}
		if len(shape.Slots) != len(st.Fields) {
			t.Fatalf("%s: %d fields, layout has %d slots", st.Name, len(st.Fields), len(shape.Slots))
		}
		for i, slot := range shape.Slots {
			if f := st.Fields[i]; f.Offset != slot.Offset || f.Type != slot.Type {
				t.Errorf("%s.%s = %s @%d, layout says %s @%d", st.Name, f.Name, f.Type, f.Offset, slot.Type, slot.Offset)
			}
		}
		if st.Size != shape.Size {
			t.Errorf("%s size %d, layout says %d", st.Name, st.Size, shape.Size)
		}
	}
}

func sample(from string) (any, bool) {
	switch from {
	case "zip.schNarrow":
		return schNarrow{}, true
	case "zip.schValidator":
		return schValidator{}, true
	case "zip.schNested":
		return schNested{}, true
	case "zip.schHeight":
		return schHeight{}, true
	}
	return nil, false
}

// Aligned, not packed — the first thing a second derivation gets wrong. A bool
// at 0 puts the word after it at 8.
func TestZAPSchema_AWordAfterAByteIsAtEight(t *testing.T) {
	s := schemaOfApp(t, func(a *App) { Get(a, "/v1/n", nop[schEmpty, schNarrow]) })
	st := structNamed(t, s, "schNarrow")
	if got := st.Fields[1].Offset; got != 8 {
		t.Fatalf("the word is at @%d, want @8 (aligned)", got)
	}
}

// bytes_fixed[N] is the exception that makes alignment worth stating apart from
// width: N bytes INLINE, aligned to 1, so an id does not push the next field to
// a 32-byte boundary. The schema states it and the REFLECTIVE encoder refuses
// it, so the op is named as needing a generated codec — not as a gap, because
// nothing about it is unexpressible.
func TestZAPSchema_AFixedArrayIsInlineAndNeedsACodec(t *testing.T) {
	s := schemaOfApp(t, func(a *App) { Get(a, "/v1/v", nop[schEmpty, schValidator]) })
	st := structNamed(t, s, "schValidator")
	if got := st.Fields[0].Type; got != "bytes_fixed[20]" {
		t.Fatalf("[20]byte rendered %q", got)
	}
	if got := st.Fields[1].Offset; got != 24 {
		t.Fatalf("the word after a 20-byte inline value is at @%d, want @24", got)
	}
	if len(s.Gaps) != 0 {
		t.Fatalf("gaps = %+v; an id is expressible", s.Gaps)
	}
	if len(s.Coded) != 1 || s.Coded[0].Field != "NodeID" || s.Coded[0].Type != "bytes_fixed[20]" {
		t.Fatalf("coded = %+v, want the id named", s.Coded)
	}
	// And the claim is about the ENCODER, not about this package's opinion of it.
	if _, err := zapencMarshal(schValidator{}); err == nil {
		t.Fatal("the reflective encoder carries it now; this case describes nothing")
	}
}

// Rendering is a pure function of the program. Two renders of one app are the
// same bytes, which is what makes the committed artifact a diff worth reading.
func TestZAPSchema_IsDeterministic(t *testing.T) {
	build := func(a *App) {
		Get(a, "/v1/height", nop[schEmpty, schHeight])
		Post(a, "/v1/holder", nop[schHolder, schHeight])
		Get(a, "/v1/validators", nop[schEmpty, schValidators])
		Get(a, "/v1/nested", nop[schEmpty, schNested])
	}
	first := schemaOfApp(t, build).String()
	for i := 0; i < 8; i++ {
		if got := schemaOfApp(t, build).String(); got != first {
			t.Fatalf("render %d differs from the first", i)
		}
	}
}

// ---- names -----------------------------------------------------------------

// The name an op carries here is the name it carries everywhere: ID(method,
// path), or the id its author wrote down. There is no second naming rule.
func TestZAPSchema_NamesComeFromTheOneIDRule(t *testing.T) {
	s := schemaOfApp(t, func(a *App) {
		Get(a, "/v1/pricing-policy", nop[schEmpty, schHeight])
		Get(a, "/v1/thing/:id", nop[schEmpty, schHeight])
		Get(a, "/v1/named", nop[schEmpty, schHeight], WithOperationID("theAuthorsName"))
	})
	// get_pricing-policy folds: the id is what ID gives, the METHOD is what the
	// lexer accepts, and the difference is recorded rather than hidden.
	want := map[string]bool{"get_pricing_policy": true, "get_thing_by_id": true, "theAuthorsName": true}
	for _, m := range methods(s) {
		if !want[m.Name] {
			t.Errorf("unexpected method %q", m.Name)
		}
		delete(want, m.Name)
	}
	for name := range want {
		t.Errorf("missing method %q", name)
	}
	if len(s.Renamed) != 1 || s.Renamed[0].Op != "get_pricing-policy" {
		t.Errorf("renamed = %+v, want the hyphenated id recorded", s.Renamed)
	}
}

// A method name is also what ID() produced, verbatim — proving the rule is
// CALLED and not re-implemented.
func TestZAPSchema_MethodNameIsIDVerbatim(t *testing.T) {
	s := schemaOfApp(t, func(a *App) { Get(a, "/v1/deep/path/:id", nop[schEmpty, schHeight]) })
	if len(methods(s)) != 1 {
		t.Fatalf("methods = %d, want 1", len(methods(s)))
	}
	if got, want := methods(s)[0].Name, ID("GET", "/v1/deep/path/:id"); got != want {
		t.Fatalf("method %q, want ID() = %q", got, want)
	}
}

// The IDL's lexer is narrower than an operationId, so an id carrying '-' or '.'
// cannot be spelled as a method name. It is folded and RECORDED, and the op that
// CAN spell its own name keeps it — a published name must not become a function
// of who else is in the room.
func TestZAPSchema_AnIDTheLexerCannotSpellIsRecorded(t *testing.T) {
	s := schemaOfApp(t, func(a *App) {
		Get(a, "/v1/credit-balance", nop[schEmpty, schHeight])
		Get(a, "/v1/credit_balance", nop[schEmpty, schNarrow])
	})
	if len(s.Renamed) != 1 {
		t.Fatalf("renamed = %+v, want exactly the hyphenated one", s.Renamed)
	}
	if got := s.Renamed[0]; got.Op != "get_credit-balance" || got.Method != "get_credit_balance2" {
		t.Fatalf("rename = %+v; want the hyphen folded and the collision ordinalled", got)
	}
	kept := false
	for _, m := range methods(s) {
		if m.Name == "get_credit_balance" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("the legitimately-named op lost its name: %+v", methods(s))
	}
	if n := len(methods(s)); n != 2 {
		t.Fatalf("methods = %d, want 2 — folding must never merge two ops", n)
	}
}

// A name the grammar already spends is not a name. A struct called `text` would
// parse as the scalar at every use, silently retyping the field.
func TestZAPSchema_AKeywordIsNotAName(t *testing.T) {
	for _, w := range []string{"text", "list", "bytes", "u64", "struct", "returns"} {
		if got := idlName(w); got != w+"_" {
			t.Errorf("idlName(%q) = %q, want it escaped", w, got)
		}
	}
	if got := idlName("Text"); got != "Text" {
		t.Errorf("idlName(%q) = %q; the guard is exact, not case-insensitive", "Text", got)
	}
}

// Two shapes may not share a name. The second is qualified by its package, then
// by an ordinal — the JSON registry's rule in the IDL's character set.
func TestZAPSchema_CollidingNamesAreQualified(t *testing.T) {
	s := schemaOfApp(t, func(a *App) {
		Get(a, "/v1/one", nop[schEmpty, struct{ A string }])
		Get(a, "/v1/two", nop[schEmpty, struct{ B string }])
	})
	if len(s.Structs) != 2 {
		t.Fatalf("structs = %d, want 2 distinct anonymous types", len(s.Structs))
	}
	if s.Structs[0].Name == s.Structs[1].Name {
		t.Fatalf("two shapes collapsed onto one name %q", s.Structs[0].Name)
	}
	e := &emitter{schema: &Schema{}, taken: map[string]bool{}}
	if got := e.name(reflect.TypeOf(schHeight{})); got != "schHeight" {
		t.Fatalf("free name = %q", got)
	}
	e.taken["schHeight"] = true
	if got := e.name(reflect.TypeOf(schHeight{})); !strings.Contains(got, "zip") {
		t.Fatalf("taken name = %q, want it qualified by its package", got)
	}
}

// ---- what the schema cannot say --------------------------------------------

// A void direction is an empty parameter list, not a struct with no fields —
// zapgen refuses `struct X {}`, and an absence is not a value.
func TestZAPSchema_VoidDirectionIsAnEmptyList(t *testing.T) {
	s := schemaOfApp(t, func(a *App) { Get(a, "/v1/ping", nop[schEmpty, schEmpty]) })
	if len(methods(s)) != 1 {
		t.Fatalf("methods = %d, want 1", len(methods(s)))
	}
	if m := methods(s)[0]; m.Request != "" || m.Reply != "" {
		t.Fatalf("method = %+v, want both directions empty", m)
	}
	if !strings.Contains(s.String(), "get_ping()\n") {
		t.Fatalf("rendered method is not a bare call:\n%s", s.String())
	}
	if len(s.Gaps) != 0 {
		t.Fatalf("a void op is not a gap: %+v", s.Gaps)
	}
}

// Each cause fires on the value that has it, and the op is ABSENT from the
// schema rather than present with the field dropped.
func TestZAPSchema_GapsByCause(t *testing.T) {
	for _, c := range []struct {
		name  string
		mount func(*App)
		op    string
		cause string
	}{
		{"map", func(a *App) { Get(a, "/v1/m", nop[schEmpty, schMapped]) }, "get_m", CauseMap},
		{"any", func(a *App) { Get(a, "/v1/a", nop[schEmpty, schAny]) }, "get_a", CauseAny},
		{"wide array", func(a *App) { Get(a, "/v1/w", nop[schEmpty, schWide]) }, "get_w", CauseUnwirable},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := schemaOfApp(t, c.mount)
			if len(methods(s)) != 0 {
				t.Errorf("op is in the schema despite a gap: %+v", methods(s))
			}
			found := false
			for _, g := range s.Gaps {
				if g.Op == c.op && g.Cause == c.cause {
					found = true
				}
			}
			if !found {
				t.Errorf("no %s gap on %s; got %+v", c.cause, c.op, s.Gaps)
			}
		})
	}
}

// A struct records every failing field, not just the first. The list's whole
// value is that it sizes the work, and stopping at the first understates it —
// which is exactly what LayoutOf does, correctly, for the encoder's question.
func TestZAPSchema_EveryFailingFieldIsNamed(t *testing.T) {
	type two struct {
		A map[string]string
		B any
		C string
	}
	if _, err := LayoutOf(reflect.TypeOf(two{})); err == nil {
		t.Fatal("the layout must refuse this type, or the diagnosis describes nothing")
	}
	s := schemaOfApp(t, func(a *App) { Get(a, "/v1/two", nop[schEmpty, two]) })
	causes := map[string]bool{}
	for _, g := range s.Gaps {
		causes[g.Cause] = true
	}
	if !causes[CauseMap] || !causes[CauseAny] {
		t.Fatalf("want both a map and an any gap, got %+v", s.Gaps)
	}
}

// One type that cannot cross blocks every op that reaches it, and each of those
// ops is named. A shared blocker is not one problem.
func TestZAPSchema_ABlockingTypeIsBlamedPerOp(t *testing.T) {
	s := schemaOfApp(t, func(a *App) {
		Get(a, "/v1/one", nop[schEmpty, schMapped])
		Get(a, "/v1/two", nop[schEmpty, schMapped])
		Get(a, "/v1/three", nop[schEmpty, schMapped])
	})
	ops := map[string]bool{}
	for _, g := range s.Gaps {
		ops[g.Op] = true
	}
	if len(ops) != 3 {
		t.Fatalf("blocked ops named = %v, want all three", ops)
	}
	if len(methods(s)) != 0 {
		t.Fatalf("methods = %+v, want none", methods(s))
	}
}

// A NESTED VALUE CROSSES WITHOUT ITS NAME. Its bytes are exact — a complete
// message in an {offset,length} slot — and the IDL's own nested struct is a
// four-byte slot, so naming the type would state a width nothing writes. The
// loss is recorded, because it measures how much shape the schema can carry.
func TestZAPSchema_ANestedValueIsOpaque(t *testing.T) {
	s := schemaOfApp(t, func(a *App) {
		Get(a, "/v1/x", nop[schEmpty, schNested])
		Get(a, "/v1/v", nop[schEmpty, schValidators])
	})
	st := structNamed(t, s, "schNested")
	if got := st.Fields[0].Type; got != "bytes" {
		t.Fatalf("nested value rendered %q, want bytes", got)
	}
	if got := st.Fields[1].Offset; got != 8 {
		t.Fatalf("the field after a nested value is at @%d, want @8", got)
	}
	// schHeight is reached ONLY as a nested value, so nothing refers to it and it
	// is not declared. A declaration nothing can name is noise.
	for _, d := range s.Structs {
		if d.Name == "schHeight" {
			t.Errorf("schHeight is declared but unreachable: nothing names a nested type")
		}
	}
	var direct, list bool
	for _, o := range s.Opaque {
		if o.Struct == "schNested" && o.Field == "Inner" && !o.List {
			direct = true
		}
		if o.Struct == "schValidators" && o.Field == "Validators" && o.List {
			list = true
		}
	}
	if !direct || !list {
		t.Fatalf("opacity = %+v, want the nested value and the list element", s.Opaque)
	}
}

// AN EMBEDDED VALUE DOES NOT CROSS, AND NOTHING FAILS. Its field name is its
// type name, so an unexported type embeds as an unexported field and the layout
// skips it — while encoding/json PROMOTES its fields to the outer object. The
// same value crosses over JSON and is absent here; the type lays out, encodes and
// decodes without complaint.
func TestZAPSchema_AnEmbeddedValueIsDroppedSilently(t *testing.T) {
	s := schemaOfApp(t, func(a *App) { Get(a, "/v1/e", nop[schEmpty, schEmbedded]) })
	st := structNamed(t, s, "schEmbedded")
	if len(st.Fields) != 1 || st.Fields[0].Name != "Extra" {
		t.Fatalf("fields = %+v, want Extra alone", st.Fields)
	}
	if got := len(wireFields(reflect.TypeOf(schEmbedded{}))); got != 2 {
		t.Fatalf("wireFields = %d, want 2: the JSON edge carries what this wire drops", got)
	}
	var found bool
	for _, d := range s.Dropped {
		if d.Struct == "schEmbedded" && d.Field == "schHeight" && d.Cause == LossPromoted {
			found = true
		}
	}
	if !found {
		t.Fatalf("dropped = %+v, want the promoted value named", s.Dropped)
	}
}

// A NESTED VALUE WHOSE OWN LAYOUT IS EMPTY CROSSES AS AN EMPTY MESSAGE. time.Time
// is the case that matters: its fields are unexported, so it takes eight bytes
// and carries nothing, and a reader cannot tell the zero it gets from a zero that
// was sent.
func TestZAPSchema_AnEmptyMessageIsDroppedSilently(t *testing.T) {
	type at struct {
		When time.Time
		Name string
	}
	s := schemaOfApp(t, func(a *App) { Get(a, "/v1/at", nop[schEmpty, at]) })
	st := structNamed(t, s, "at")
	if len(st.Fields) != 2 {
		t.Fatalf("fields = %+v; the instant still takes a slot", st.Fields)
	}
	var found bool
	for _, d := range s.Dropped {
		if d.Struct == "at" && d.Field == "When" && d.Cause == LossEmpty {
			found = true
		}
	}
	if !found {
		t.Fatalf("dropped = %+v, want the instant named", s.Dropped)
	}
	// And the claim is about the WIRE, not about this package's opinion of it.
	shape, err := LayoutOf(reflect.TypeOf(time.Time{}))
	if err != nil || len(shape.Slots) != 0 {
		t.Fatalf("time.Time lays out %d slots (err %v); this case describes nothing", len(shape.Slots), err)
	}
}

// json.RawMessage crosses EXACTLY, as bytes. It is not a gap and not a loss —
// the shape is simply unstated, which is what the schema is for.
func TestZAPSchema_RawJSONCrossesAsBytes(t *testing.T) {
	s := schemaOfApp(t, func(a *App) { Get(a, "/v1/r", nop[schEmpty, schRaw]) })
	st := structNamed(t, s, "schRaw")
	if got := st.Fields[0].Type; got != "bytes" {
		t.Fatalf("json.RawMessage rendered %q, want bytes", got)
	}
	if len(s.Gaps) != 0 || len(s.Dropped) != 0 {
		t.Fatalf("gaps=%+v dropped=%+v; the bytes cross", s.Gaps, s.Dropped)
	}
}

// ---- the fleet shape --------------------------------------------------------

// One interface per app, one struct namespace across them. A type both apps
// reach is declared once and referred to by name from both, because a type
// belongs to itself while an operation belongs to its service.
func TestZAPSchema_OneInterfacePerApp(t *testing.T) {
	one, two := New(Config{AppName: "search"}), New(Config{AppName: "ai"})
	Get(one, "/v1/search/height", nop[schEmpty, schHeight])
	Get(two, "/v1/ai/height", nop[schEmpty, schHeight])
	Post(two, "/v1/ai/vals", nop[schEmpty, schValidators])

	s := ZAPSchema("cloud", one, two)
	if len(s.Interfaces) != 2 {
		t.Fatalf("interfaces = %d, want 2", len(s.Interfaces))
	}
	if s.Interfaces[0].Name != "ai" || s.Interfaces[1].Name != "search" {
		t.Fatalf("interfaces = %q %q, want ai then search", s.Interfaces[0].Name, s.Interfaces[1].Name)
	}
	if s.Ops() != 3 {
		t.Fatalf("ops = %d, want 3", s.Ops())
	}
	n := 0
	for _, st := range s.Structs {
		if st.Name == "schHeight" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("schHeight declared %d times across two apps, want once", n)
	}
	if !strings.Contains(s.String(), "interface ai {") || !strings.Contains(s.String(), "interface search {") {
		t.Fatalf("both interfaces must be rendered:\n%s", s.String())
	}
}

// An app whose every op is blocked contributes no interface at all. An empty
// `interface x {}` says a service exists and answers nothing, which is a
// different claim from "this service could not be expressed".
func TestZAPSchema_AnEntirelyBlockedAppHasNoInterface(t *testing.T) {
	a := New(Config{AppName: "blocked"})
	Get(a, "/v1/m", nop[schEmpty, schMapped])
	s := ZAPSchema("cloud", a)
	if len(s.Interfaces) != 0 {
		t.Fatalf("interfaces = %+v, want none", s.Interfaces)
	}
	if s.Blocked() != 1 {
		t.Fatalf("blocked = %d, want 1", s.Blocked())
	}
}

// ---- helpers ---------------------------------------------------------------

// zapencMarshal is the reflective encoder the op-call plane calls (call.go), so
// a case about what it refuses asks IT rather than restating its rules.
func zapencMarshal(v any) ([]byte, error) { return zapenc.Marshal(v) }

// methods flattens the per-app interfaces, for cases built on one app.
func methods(s *Schema) []*Method {
	var out []*Method
	for _, i := range s.Interfaces {
		out = append(out, i.Methods...)
	}
	return out
}

func structNamed(t *testing.T, s *Schema, name string) *Struct {
	t.Helper()
	for _, st := range s.Structs {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("no struct %q in:\n%s", name, s.String())
	return nil
}
