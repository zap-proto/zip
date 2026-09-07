package zip

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeBin(t *testing.T, dir, name, body string) (string, string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return p, hex.EncodeToString(sum[:])
}

// A binary read off disk gets the same check as one off a network. Verifying
// only the download checks the case that looks dangerous rather than the one
// that is: an attacker who can write the plugin directory has already done the
// download for you.
func TestDiskBinaryIsVerified(t *testing.T) {
	dir := t.TempDir()
	path, sum := writeBin(t, dir, "svc", "#!/bin/true\n")

	b, err := openVerified(path, t.TempDir(), "svc", sum)
	if err != nil {
		t.Fatalf("matching digest must be accepted: %v", err)
	}
	b.Close()

	if _, err := openVerified(path, t.TempDir(), "svc", strings.Repeat("0", 64)); err == nil {
		t.Fatal("a binary whose digest does not match Sum must be refused, not logged")
	} else if !strings.Contains(err.Error(), "refusing to run it") {
		t.Fatalf("refusal must say so plainly: %v", err)
	}
}

// Changing the original after it is verified must not change what runs — and
// the two ways to change it are different. A rename swaps the inode; a write
// keeps it, and a write IS visible through an already-open descriptor. Holding
// a descriptor to the original would defend against the first and not the
// second, which is why the bytes are copied out of reach first.
func TestSwapAfterVerifyDoesNotChangeWhatRuns(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("executing a descriptor is Linux-only; elsewhere the digest still gates the start")
	}
	dir := t.TempDir()
	path, sum := writeBin(t, dir, "svc", "original")

	b, err := openVerified(path, t.TempDir(), "svc", sum)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// In place, same inode: the cheap attack, and the one a held descriptor
	// on the ORIGINAL would not have caught.
	if err := os.WriteFile(path, []byte("substituted"), 0o700); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, len("original"))
	if _, err := b.f.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("the descriptor read %q; it must still see the verified bytes", got)
	}

	cmd := b.exec(nil)
	if cmd.Path != "/proc/self/fd/3" {
		t.Fatalf("cmd.Path = %q, want the verified descriptor", cmd.Path)
	}
	if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != b.f {
		t.Fatal("the verified descriptor must be the one passed to the child")
	}
	if len(cmd.Args) == 0 || cmd.Args[0] != "svc" {
		t.Fatalf("argv[0] = %v, want the plugin's name — the descriptor is how the image is found, not what it is called", cmd.Args)
	}
}

// An unset Sum still loads: a host that has not been given a digest is not
// told a lie about having checked one. What must never happen is a Sum that is
// set and not enforced.
func TestNoSumStillLoads(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeBin(t, dir, "svc", "whatever")
	b, err := openVerified(path, t.TempDir(), "svc", "")
	if err != nil {
		t.Fatalf("a plugin with no declared digest must still start: %v", err)
	}
	b.Close()
}
