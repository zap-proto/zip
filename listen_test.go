package zip

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// One serving mechanism, two spellings. Listen IS Serve followed by Wait, so
// there is no second path to a running program — only a terminal form that
// hands back no handle, and says so.
func TestListenIsServePlusWait(t *testing.T) {
	sock := filepath.Join(sockDir(t), "a.sock")
	app := quiet("svc")
	app.Get("/x", func(c *Ctx) error { return c.String(200, "x") })

	done := make(chan error, 1)
	go func() { done <- app.Listen(sock) }()

	// Listen must BLOCK while serving. A version of this that returned nil
	// immediately still passed the shutdown assertion below, so the test has to
	// prove the socket accepts and that Listen has not returned yet.
	waitBound(t, sock)
	select {
	case err := <-done:
		t.Fatalf("Listen returned while it should be serving: %v", err)
	default:
	}

	// It is serving, and it is a generation like any other.
	if n, live := app.Generation(); !live || n != 0 {
		t.Errorf("Listen did not install generation 0: n=%d live=%v", n, live)
	}
	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-done: // Listen returned when the listeners stopped
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not return after Shutdown")
	}
}

// Serve hands back the handle Listen withholds, and the handle is what makes a
// running program changeable.
func TestServeYieldsAHandleListenDoesNot(t *testing.T) {
	sock := filepath.Join(sockDir(t), "b.sock")
	app := quiet("svc")
	app.Get("/x", func(c *Ctx) error { return c.String(200, "x") })

	h, err := Serve(app, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer func() { _ = h.Close() }()
	waitBound(t, sock)

	later := quiet("later")
	later.Get("/later", func(c *Ctx) error { return nil })
	if err := h.Include(later); err != nil {
		t.Fatalf("Include on a served program: %v", err)
	}
	if n := h.Generation(); n != 1 {
		t.Errorf("generation = %d after Include, want 1", n)
	}
}

// waitBound dials until the socket accepts. App.listening is incremented when
// the servers are CONSTRUCTED, before any of them has touched a socket, so it
// answers a different question than "is this reachable".
func waitBound(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("unix", sock)
		if err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never bound %s: %v", sock, err)
		}
		time.Sleep(time.Millisecond)
	}
}
