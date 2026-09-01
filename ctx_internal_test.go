package zip

import "testing"

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
