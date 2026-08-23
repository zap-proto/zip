package zip

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestALongSocketNamesTheLimitRatherThanTheErrno is the failure this cost an
// afternoon to read once already: the sibling's address is DERIVED, so a caller
// that picked a fine socket path can still be over the limit by the five bytes
// the suffix adds — and the bind reported "invalid argument", which names neither
// the limit nor the length.
func TestALongSocketNamesTheLimitRatherThanTheErrno(t *testing.T) {
	dir, err := os.MkdirTemp("", "z")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Long enough that the derived ".http" sibling cannot fit.
	long := filepath.Join(dir, strings.Repeat("n", 120)+".sock")

	app := New(Config{DisableStartupMessage: true})
	got := app.listenOn([]string{long})
	if got == nil {
		t.Fatal("a socket past the limit must be refused")
	}
	msg := got.Error()
	for _, want := range []string{"limit", "sibling"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "invalid argument") {
		t.Errorf("error = %q — that is the errno this replaces", msg)
	}
}
