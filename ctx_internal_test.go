package zip

import (
	"testing"
	"unsafe"
)

// Text that is not valid encoding is passed through rather than refused. It
// cannot be sent through this package's own test client — net/url refuses to
// parse a lone percent — so the decision is pinned here, where the function is.
func TestSegmentPassesThroughWhatItCannotDecode(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain", "plain"},
		{"a%20b", "a b"},
		{"a%2Fb", "a/b"},
		{"caf%C3%A9", "café"},
		{"100%25", "100%"},
		{"100%", "100%"}, // a lone percent: malformed, passed through
		{"%zz", "%zz"},   // not hex either
		{"%", "%"},
		{"", ""},
	} {
		if got := segment(c.in); got != c.want {
			t.Errorf("segment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// detach reaches every string a binder can write: a field, through a pointer,
// in a slice, an array, a map's keys and values, and behind an interface. The
// strings start as views of one buffer, the way a fiber binder writes them;
// after detach the buffer is overwritten and none of them may move.
func TestDetachCopiesEveryReachableString(t *testing.T) {
	buf := []byte("aaaa")
	view := unsafe.String(&buf[0], len(buf))
	type node struct {
		S    string
		P    *string
		L    []string
		A    [1]string
		M    map[string]string
		I    any
		Next *node
		Nil  *node
		None map[string]string
		Zero any
		hid  string // no binder writes one, so the walk leaves it be
	}
	s := view
	n := &node{
		S: view, P: &s, L: []string{view}, A: [1]string{view},
		M: map[string]string{view: view}, I: map[string]any{"k": view}, hid: view,
	}
	n.Next = n                         // a cycle ends
	m := map[string]string{view: view} // a map passed by value, as a binder may be given one
	detach(n)
	detach(m)
	copy(buf, "bbbb")

	got := []string{n.S, *n.P, n.L[0], n.A[0], n.M["aaaa"], n.I.(map[string]any)["k"].(string), m["aaaa"]}
	for k := range n.M {
		got = append(got, k)
	}
	for k := range m {
		got = append(got, k)
	}
	for i, g := range got {
		if g != "aaaa" {
			t.Errorf("string %d reads %q after its buffer was reused, want %q", i, g, "aaaa")
		}
	}
}
