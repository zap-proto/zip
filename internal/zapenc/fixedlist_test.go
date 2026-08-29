package zapenc

import (
	"reflect"
	"strings"
	"testing"
)

// A list of ids is the shape every chain reply is full of, and it is exactly
// the shape bytes_fixed exists for. The element has to keep its WIDTH — the
// schema is the contract a generated codec is built from, and bytes_fixed[0]
// is a contract to read nothing — and the refusal has to be the one that says
// what to do about it, not the one that says a kind has no wire form.
type fixedList struct {
	Nodes  [][20]byte
	Chains [][32]byte
}

func TestFixedListElementKeepsItsWidth(t *testing.T) {
	sh, err := LayoutOf(reflect.TypeOf(fixedList{}))
	if err != nil {
		t.Fatalf("LayoutOf: %v", err)
	}
	want := map[string]string{
		"Nodes":  "list<bytes_fixed[20]>",
		"Chains": "list<bytes_fixed[32]>",
	}
	for _, s := range sh.Slots {
		if s.Type != want[s.Name] {
			t.Errorf("%s: schema says %s, want %s", s.Name, s.Type, want[s.Name])
		}
		if s.Elem != strings.TrimSuffix(strings.TrimPrefix(want[s.Name], "list<"), ">") {
			t.Errorf("%s: element says %q", s.Name, s.Elem)
		}
	}
}

func TestFixedListRefusalNamesTheFix(t *testing.T) {
	_, err := Marshal(&fixedList{Nodes: [][20]byte{{1}}})
	if err == nil {
		t.Fatal("the reflective codec must refuse bytes_fixed, in a list as in a field")
	}
	if !strings.Contains(err.Error(), "MarshalZAP") {
		t.Fatalf("the refusal has to name the fix, got: %v", err)
	}
}
