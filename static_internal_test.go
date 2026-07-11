package zip

import "testing"

// staticClean is the fail-closed traversal guard, tested directly so the
// security property holds regardless of what URL normalization the transport
// does upstream: any ".." escape or non-fs.ValidPath input is rejected, while
// in-root navigation ("a/../b") collapses and is allowed.
func TestStaticClean_FailClosed(t *testing.T) {
	cases := []struct {
		sub, index string
		wantName   string
		wantOK     bool
	}{
		{"main.css", "", "main.css", true},
		{"a/b/c.js", "", "a/b/c.js", true},
		{"a/../b.js", "", "b.js", true},        // in-root nav collapses — allowed
		{"./x.css", "", "x.css", true},         // leading . collapses
		{"", "", "", true},                     // root, no index → nothing to serve
		{"", "index.html", "index.html", true}, // root → index
		{"/", "", "", true},                    // just a slash → root
		{"..", "", "", false},                  // bare escape
		{"../secret", "", "", false},           // escape one level
		{"../../etc/passwd", "", "", false},
		{"a/../../secret", "", "", false}, // escapes after collapse
		{"sub/../../secret", "", "", false},
	}
	for _, c := range cases {
		name, ok := staticClean(c.sub, c.index)
		if ok != c.wantOK || name != c.wantName {
			t.Errorf("staticClean(%q, %q) = (%q, %v), want (%q, %v)",
				c.sub, c.index, name, ok, c.wantName, c.wantOK)
		}
	}
}
