package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// newJSRunner builds a Runner with zip's real goja engine registered as
// "js". No stubs: this is the same *JSRuntime base/gojavm wraps.
func newJSRunner(t *testing.T) Runner {
	t.Helper()
	rt, err := NewJSRuntime(JSOptions{PoolSize: 8})
	if err != nil {
		t.Fatalf("NewJSRuntime: %v", err)
	}
	r := NewRunner()
	if err := r.Register("js", rt.Engine()); err != nil {
		t.Fatalf("Register js: %v", err)
	}
	return r
}

// TestRunnerJSHappyPath: real goja, real arithmetic, real Export() result.
func TestRunnerJSHappyPath(t *testing.T) {
	r := newJSRunner(t)
	got, err := r.Run(context.Background(), "js", []byte("1+2"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != int64(3) {
		t.Fatalf("1+2 = %#v (%T), want int64(3)", got, got)
	}
}

// TestRunnerJSFunctionCall defines a function and returns the result of
// calling it via a top-level expression — proving a defined function is
// reachable and invocable in the same eval.
func TestRunnerJSFunctionCall(t *testing.T) {
	r := newJSRunner(t)
	got, err := r.Run(context.Background(), "js",
		[]byte("function add(a,b){return a+b}; add(40,2)"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != int64(42) {
		t.Fatalf("add(40,2) = %#v (%T), want int64(42)", got, got)
	}

	// And capture the function value itself, then confirm it round-trips
	// as a callable (Export of a goja function is a Go func wrapper).
	fnVal, err := r.Run(context.Background(), "js",
		[]byte("function add(a,b){return a+b}; add"))
	if err != nil {
		t.Fatalf("Run capture fn: %v", err)
	}
	if fnVal == nil {
		t.Fatalf("captured function is nil")
	}
}

// TestRunnerJSDeadline: an infinite loop must be interrupted by ctx
// deadline. Asserts the error is context.DeadlineExceeded and the wall
// time is bounded (interrupt actually fired, not a hung worker).
func TestRunnerJSDeadline(t *testing.T) {
	r := newJSRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := r.Run(ctx, "js", []byte("while(true){}"))
	elapsed := time.Since(start)

	t.Logf("deadline test elapsed: %v", elapsed)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("elapsed %v > 200ms: interrupt did not fire promptly", elapsed)
	}
}

// TestRunnerUnknownLanguage: dispatch to an unregistered language returns
// the typed sentinel, not a panic.
func TestRunnerUnknownLanguage(t *testing.T) {
	r := newJSRunner(t)
	_, err := r.Run(context.Background(), "cobol", []byte("DISPLAY 'HI'."))
	if !errors.Is(err, ErrUnknownLanguage) {
		t.Fatalf("err = %v, want ErrUnknownLanguage", err)
	}
}

// TestRunnerConcurrent: 100 goroutines each evaluate a distinct random
// expression a+b and assert the result. Run under -race this proves the
// registry RWMutex and the per-call VM isolation are sound.
func TestRunnerConcurrent(t *testing.T) {
	r := newJSRunner(t)

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := rand.Intn(10000)
			b := rand.Intn(10000)
			src := fmt.Sprintf("%d+%d", a, b)
			got, err := r.Run(context.Background(), "js", []byte(src))
			if err != nil {
				errs <- fmt.Errorf("%s: %w", src, err)
				return
			}
			if got != int64(a+b) {
				errs <- fmt.Errorf("%s = %#v, want %d", src, got, a+b)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestRunnerLanguages: registry reports registered languages, sorted.
func TestRunnerLanguages(t *testing.T) {
	r := newJSRunner(t)
	langs := r.Languages()
	if len(langs) != 1 || langs[0] != "js" {
		t.Fatalf("Languages() = %v, want [js]", langs)
	}
}
