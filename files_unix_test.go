//go:build linux || darwin

package zip

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// A descriptor that crosses is proven by READING THROUGH IT, never by counting
// the files that came back. A receiver can hold the right number of handles
// that all point at the wrong thing, and a count cannot tell the difference.
func TestFileCrossesTheSocket(t *testing.T) {
	a, b := unixPair(t)

	path := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(path, []byte("through the descriptor"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := SendFiles(a, []byte("x"), []*os.File{f}); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}
	buf := make([]byte, 8)
	n, files, err := RecvFiles(b, buf)
	if err != nil {
		t.Fatalf("RecvFiles: %v", err)
	}
	if n != 1 || string(buf[:n]) != "x" {
		t.Fatalf("bytes = %q, want \"x\"", buf[:n])
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	defer files[0].Close()

	got, err := io.ReadAll(files[0])
	if err != nil {
		t.Fatalf("read through received fd: %v", err)
	}
	if string(got) != "through the descriptor" {
		t.Fatalf("read %q through the received descriptor", got)
	}

	// The two descriptors are independent: closing the sender's does not
	// disturb the receiver's, which is the property that makes handing over a
	// console or a gofer connection safe.
	_ = f.Close()
	if _, err := files[0].Seek(0, io.SeekStart); err != nil {
		t.Fatalf("received fd died with the sender's: %v", err)
	}
}

// A message with no files still delivers its bytes, so a transport does not
// need two paths for "with" and "without".
func TestBytesWithoutFiles(t *testing.T) {
	a, b := unixPair(t)
	if err := SendFiles(a, []byte("plain"), nil); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, files, err := RecvFiles(b, buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "plain" || len(files) != 0 {
		t.Fatalf("got %q with %d files", buf[:n], len(files))
	}
}

// Refusing is the whole point of the error: delivering the bytes while dropping
// the files would be a message that lies about what it carried.
func TestNonUnixRefuses(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no tcp")
	}
	defer l.Close()
	done := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); done <- c }()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer func() { (<-done).Close() }()

	if err := SendFiles(c, []byte("x"), []*os.File{os.Stdin}); !errors.Is(err, ErrNoFilePassing) {
		t.Fatalf("SendFiles over tcp = %v, want ErrNoFilePassing", err)
	}
}

// Control-only messages can be discarded before delivery, so an empty body is
// refused rather than sent and usually working.
func TestEmptyBodyRefused(t *testing.T) {
	a, _ := unixPair(t)
	if err := SendFiles(a, nil, []*os.File{os.Stdin}); err == nil {
		t.Fatal("SendFiles accepted an empty body")
	}
}

func unixPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	dir := t.TempDir()
	addr := filepath.Join(dir, "s")
	l, err := net.Listen("unix", addr)
	if err != nil {
		t.Skip("no unix sockets here")
	}
	t.Cleanup(func() { l.Close() })
	ch := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); ch <- c }()
	a, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	b := <-ch
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}
