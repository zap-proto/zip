package zip

import (
	"os"
	"testing"
)

// sockDir is a directory a unix socket can bind in.
//
// t.TempDir cannot be used for one: a socket address is 104 bytes on Darwin
// (108 on Linux) and t.TempDir spends most of them on the TEST'S OWN NAME, so a
// test with a descriptive name fails to bind with EINVAL — reported as "never
// began listening", which reads like a timing bug and is not. It is green on
// Linux, red on every Mac, and a test that passes today breaks when a test is
// RENAMED.
func sockDir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("", "zt")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}
