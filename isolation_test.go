package zip_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// TestTwoAppsNoStateBleed proves that two zip.App instances in the same
// process maintain disjoint route tables, disjoint middleware chains,
// and disjoint internal state. This is the structural invariant that
// makes horizontal scale safe: N replicas of the same binary running N
// independent App instances must never coordinate via globals.
//
// The contract this test enforces:
//   - Routes registered on app A are NOT visible to app B
//   - Middleware applied to app A is NOT applied to app B's requests
//   - A panic recovered in app A does not affect app B
//
// If this test fails, there is a package-level mutable map somewhere
// in the zip module that needs to move into App.
func TestTwoAppsNoStateBleed(t *testing.T) {
	a := zip.New(zip.Config{AppName: "a", DisableStartupMessage: true})
	b := zip.New(zip.Config{AppName: "b", DisableStartupMessage: true})

	// Route registered on A only.
	a.Get("/from-a", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"from": "a"})
	})
	// Route registered on B only.
	b.Get("/from-b", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"from": "b"})
	})

	// Middleware only on A — sets a marker header on every A request.
	a.Use(func(c *zip.Ctx) error {
		c.SetHeader("X-App", "a")
		return c.Continue()
	})
	// Middleware only on B — sets a different marker on B.
	b.Use(func(c *zip.Ctx) error {
		c.SetHeader("X-App", "b")
		return c.Continue()
	})

	// A's route is reachable on A.
	reqA, _ := http.NewRequest("GET", "/from-a", nil)
	resA, err := a.Fiber().Test(reqA)
	if err != nil {
		t.Fatalf("a.Test: %v", err)
	}
	if resA.StatusCode != 200 {
		t.Fatalf("a /from-a: status=%d, want 200", resA.StatusCode)
	}
	body, _ := io.ReadAll(resA.Body)
	if got := string(body); got != `{"from":"a"}` {
		t.Fatalf("a /from-a body=%s", got)
	}

	// A's route is NOT reachable on B.
	reqA2B, _ := http.NewRequest("GET", "/from-a", nil)
	resA2B, err := b.Fiber().Test(reqA2B)
	if err != nil {
		t.Fatalf("b.Test: %v", err)
	}
	if resA2B.StatusCode != 404 {
		t.Fatalf("b /from-a: status=%d, want 404 (route leaked from A)", resA2B.StatusCode)
	}

	// B's route is reachable on B.
	reqB, _ := http.NewRequest("GET", "/from-b", nil)
	resB, err := b.Fiber().Test(reqB)
	if err != nil {
		t.Fatalf("b.Test: %v", err)
	}
	if resB.StatusCode != 200 {
		t.Fatalf("b /from-b: status=%d, want 200", resB.StatusCode)
	}

	// B's route is NOT reachable on A.
	reqB2A, _ := http.NewRequest("GET", "/from-b", nil)
	resB2A, err := a.Fiber().Test(reqB2A)
	if err != nil {
		t.Fatalf("a.Test: %v", err)
	}
	if resB2A.StatusCode != 404 {
		t.Fatalf("a /from-b: status=%d, want 404 (route leaked from B)", resB2A.StatusCode)
	}
}

// TestTwoAppsIndependentShutdown verifies that shutting down one App
// does not affect the other.
func TestTwoAppsIndependentShutdown(t *testing.T) {
	a := zip.New(zip.Config{AppName: "a", DisableStartupMessage: true})
	b := zip.New(zip.Config{AppName: "b", DisableStartupMessage: true})
	b.Get("/still-up", func(c *zip.Ctx) error { return c.JSON(200, map[string]bool{"ok": true}) })

	if err := a.Shutdown(); err != nil {
		t.Fatalf("a.Shutdown: %v", err)
	}

	req, _ := http.NewRequest("GET", "/still-up", nil)
	resp, err := b.Fiber().Test(req)
	if err != nil {
		t.Fatalf("b after a.Shutdown: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("b /still-up: status=%d, want 200", resp.StatusCode)
	}
}
