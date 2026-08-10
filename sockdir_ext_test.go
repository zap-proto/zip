package zip_test

import (
	"os"
	"testing"
)

// sockDir is the external-test-package twin of the helper in sockdir_test.go.
// Two test packages compile separately, so the helper exists in each; the doc
// comment lives once, beside the internal copy.
func sockDir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("", "zt")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}
