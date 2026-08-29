package zip

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// quoted is the shape that made this necessary: a number carried as a quoted
// decimal, because JSON numbers are float64 and a uint64 above 2^53 does not
// survive one. It writes its own JSON, so its fields are not the wire form.
type quoted uint64

func (q quoted) MarshalJSON() ([]byte, error) { return []byte(`"0"`), nil }

func (quoted) JSONSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": "^[0-9]+$"}
}

// silent marshals itself and says nothing about its shape — the case that must
// still fall back rather than guess.
type silent uint64

func (silent) MarshalJSON() ([]byte, error) { return []byte(`"0"`), nil }

// onPointer declares the shape on the pointer, which is where MarshalJSON is
// usually written.
type onPointer struct{ n uint64 }

func (o *onPointer) MarshalJSON() ([]byte, error) { return []byte(`"0"`), nil }
func (o *onPointer) JSONSchema() map[string]any   { return map[string]any{"type": "string"} }

type nilSchema uint64

func (nilSchema) MarshalJSON() ([]byte, error) { return []byte(`0`), nil }
func (nilSchema) JSONSchema() map[string]any   { return nil }

func TestDeclaredSchemaBeatsTheEmptyObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   reflect.Type
		want map[string]any
	}{
		{"a marshaler that states its shape", reflect.TypeOf(quoted(0)),
			map[string]any{"type": "string", "pattern": "^[0-9]+$"}},
		{"declared on the pointer", reflect.TypeOf(onPointer{}),
			map[string]any{"type": "string"}},
		{"a marshaler that states nothing stays unconstrained", reflect.TypeOf(silent(0)),
			map[string]any{}},
		{"a nil answer is not a shape", reflect.TypeOf(nilSchema(0)),
			map[string]any{}},
		{"time.Time keeps the shape it already had", reflect.TypeOf(time.Time{}),
			map[string]any{"type": "string", "format": "date-time"}},
	} {
		got := schemaOf(tc.in, nil, nil)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// The document is assembled by mutation, so a type's answer must not be the
// map the document then edits — one field's constraints would leak into every
// other field of the same type.
func TestTheTypesAnswerIsNotShared(t *testing.T) {
	a := schemaOf(reflect.TypeOf(quoted(0)), nil, nil)
	a["description"] = "the first field"
	b := schemaOf(reflect.TypeOf(quoted(0)), nil, nil)
	if _, leaked := b["description"]; leaked {
		t.Fatal("editing one field's schema changed the next one")
	}
}

// The point of the whole exercise: a reply carrying such a field describes it,
// instead of publishing an untyped value a generated client cannot use.
func TestAReplyFieldIsDescribed(t *testing.T) {
	type reply struct {
		Height quoted `json:"height"`
	}
	got := rootSchemaOf(reflect.TypeOf(reply{}), nil)
	props, _ := got["properties"].(map[string]any)
	h, _ := props["height"].(map[string]any)
	if h["type"] != "string" {
		b, _ := json.Marshal(got)
		t.Fatalf("height was not described: %s", b)
	}
}
