package middleware_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// TestBreaker_OpenAfterThreshold verifies the breaker opens after N
// consecutive failures and serves 503 thereafter.
func TestBreaker_OpenAfterThreshold(t *testing.T) {
	b := middleware.NewBreaker(middleware.BreakerConfig{
		FailureThreshold: 3,
		OpenWindow:       50 * time.Millisecond,
	})
	for i := 0; i < 3; i++ {
		if !b.Allow() {
			t.Fatalf("iter %d: Allow returned false while closed", i)
		}
		b.Report(errors.New("boom"), 500)
	}
	if b.State() != middleware.BreakerOpen {
		t.Fatalf("state=%d, want Open", b.State())
	}
	if b.Allow() {
		t.Fatalf("Allow returned true while open")
	}
}

// TestBreaker_HalfOpenAfterWindow verifies that after OpenWindow the
// breaker transitions to half-open and admits exactly one request.
func TestBreaker_HalfOpenAfterWindow(t *testing.T) {
	now := time.Unix(0, 0)
	nowFn := func() time.Time { return now }
	b := middleware.NewBreaker(middleware.BreakerConfig{
		FailureThreshold: 1,
		OpenWindow:       100 * time.Millisecond,
		Now:              nowFn,
	})
	// Trip open.
	if !b.Allow() {
		t.Fatal("Allow false on first call")
	}
	b.Report(errors.New("boom"), 500)
	if s := b.State(); s != middleware.BreakerOpen {
		t.Fatalf("state=%d, want Open", s)
	}
	// Advance past OpenWindow.
	now = now.Add(200 * time.Millisecond)
	// First Allow in half-open should succeed.
	if !b.Allow() {
		t.Fatalf("first half-open Allow returned false")
	}
	// Second concurrent Allow should be denied.
	if b.Allow() {
		t.Fatalf("second concurrent half-open Allow returned true")
	}
	// A success closes the breaker.
	b.Report(nil, 200)
	if s := b.State(); s != middleware.BreakerClosed {
		t.Fatalf("state after half-open success = %d, want Closed", s)
	}
}

// TestBreaker_HalfOpenFailureReopens verifies a half-open failure
// re-opens the breaker.
func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	now := time.Unix(0, 0)
	nowFn := func() time.Time { return now }
	b := middleware.NewBreaker(middleware.BreakerConfig{
		FailureThreshold: 1,
		OpenWindow:       100 * time.Millisecond,
		Now:              nowFn,
	})
	_ = b.Allow()
	b.Report(errors.New("boom"), 500)
	now = now.Add(200 * time.Millisecond)
	if !b.Allow() {
		t.Fatalf("half-open Allow false")
	}
	b.Report(errors.New("still broken"), 500)
	if s := b.State(); s != middleware.BreakerOpen {
		t.Fatalf("state after half-open failure = %d, want Open", s)
	}
}

// TestBreaker_Middleware_HappyPath confirms the zip middleware passes
// successful requests through.
func TestBreaker_Middleware_HappyPath(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	b := middleware.NewBreaker(middleware.BreakerConfig{FailureThreshold: 3})
	app.Use(b.Middleware())
	app.Get("/ok", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]bool{"ok": true})
	})
	req, _ := http.NewRequest("GET", "/ok", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
}

// TestBreaker_Middleware_ShortCircuits confirms the breaker returns
// 503 once tripped.
func TestBreaker_Middleware_ShortCircuits(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	b := middleware.NewBreaker(middleware.BreakerConfig{
		FailureThreshold: 2,
		OpenWindow:       time.Hour, // effectively never re-opens for the test
	})
	app.Use(b.Middleware())
	app.Get("/fail", func(c *zip.Ctx) error {
		return zip.Errorf(500, "broken")
	})
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("GET", "/fail", nil)
		resp, _ := app.Fiber().Test(req)
		if resp.StatusCode != 500 {
			t.Fatalf("iter %d: status=%d, want 500", i, resp.StatusCode)
		}
	}
	// Next call should short-circuit with 503.
	req, _ := http.NewRequest("GET", "/fail", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("short-circuit status=%d, want 503", resp.StatusCode)
	}
}

// TestBreaker_StateChangeCallback verifies OnStateChange fires.
func TestBreaker_StateChangeCallback(t *testing.T) {
	var mu sync.Mutex
	var transitions []middleware.BreakerState
	b := middleware.NewBreaker(middleware.BreakerConfig{
		FailureThreshold: 1,
		OpenWindow:       10 * time.Millisecond,
		OnStateChange: func(_, next middleware.BreakerState) {
			mu.Lock()
			defer mu.Unlock()
			transitions = append(transitions, next)
		},
	})
	_ = b.Allow()
	b.Report(errors.New("boom"), 500)
	// Wait long enough for OpenWindow to elapse, then call State() to
	// trigger the half-open transition.
	time.Sleep(20 * time.Millisecond)
	_ = b.State()
	mu.Lock()
	defer mu.Unlock()
	if len(transitions) < 2 {
		t.Fatalf("transitions=%v, want at least 2 (Open, HalfOpen)", transitions)
	}
	if transitions[0] != middleware.BreakerOpen || transitions[1] != middleware.BreakerHalfOpen {
		t.Fatalf("transitions=%v, want [Open, HalfOpen, ...]", transitions)
	}
}

// TestBreaker_Stats exposes counter semantics.
func TestBreaker_Stats(t *testing.T) {
	b := middleware.NewBreaker(middleware.BreakerConfig{
		FailureThreshold: 2,
		OpenWindow:       time.Hour,
	})
	_ = b.Allow()
	b.Report(nil, 200)
	_ = b.Allow()
	b.Report(errors.New("boom"), 500)
	_ = b.Allow()
	b.Report(errors.New("boom"), 500)
	// Now open. Two more Allow calls — both short-circuit.
	_ = b.Allow()
	_ = b.Allow()
	s := b.Stats()
	if s.Successes != 1 {
		t.Fatalf("successes=%d, want 1", s.Successes)
	}
	if s.Failures != 2 {
		t.Fatalf("failures=%d, want 2", s.Failures)
	}
	if s.ShortCircuited != 2 {
		t.Fatalf("short_circuited=%d, want 2", s.ShortCircuited)
	}
}
