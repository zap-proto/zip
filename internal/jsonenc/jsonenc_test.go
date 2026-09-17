package jsonenc

import "testing"

// The wire is a contract with every client, so it must not move with the
// toolchain. Each case below is a place where encoding/json/v2 answers
// differently, and Go 1.27 turned v2 on by default.
func TestWireDoesNotFollowTheToolchain(t *testing.T) {
	type shape struct {
		N    int            `json:"n,omitempty"`
		On   bool           `json:"on,omitempty"`
		List []int          `json:"list"`
		Map  map[string]int `json:"map"`
	}
	got, err := Marshal(shape{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"list":null,"map":null}`; string(got) != want {
		t.Fatalf("Marshal(zero) = %s, want %s", got, want)
	}

	// Map keys come out sorted, so a document generated twice is the same bytes.
	// Fourteen keys make an unsorted encoder matching by chance negligible.
	keys := map[string]int{}
	for _, k := range []string{"q", "w", "e", "r", "t", "y", "u", "i", "o", "p", "a", "s", "d", "f"} {
		keys[k] = 1
	}
	got, err = Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":1,"d":1,"e":1,"f":1,"i":1,"o":1,"p":1,"q":1,"r":1,"s":1,"t":1,"u":1,"w":1,"y":1}`; string(got) != want {
		t.Fatalf("Marshal(map) = %s, want sorted keys", got)
	}

	var in struct {
		Name string `json:"name"`
	}
	if err := Unmarshal([]byte(`{"Name":"a"}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.Name != "a" {
		t.Fatalf("a field name that differs only in case was dropped: %+v", in)
	}
}
