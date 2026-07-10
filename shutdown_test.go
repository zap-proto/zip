package zip_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestOnShutdown_LIFO proves hooks drain in reverse registration order
// (reverse mount order) — the natural teardown of dependencies. Hooks run
// sequentially in Shutdown's goroutine, so a plain slice needs no lock.
func TestOnShutdown_LIFO(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	var order []int
	for i := 1; i <= 3; i++ {
		i := i
		app.OnShutdown(func(context.Context) error {
			order = append(order, i)
			return nil
		})
	}

	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	want := []int{3, 2, 1}
	if len(order) != len(want) {
		t.Fatalf("order=%v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order=%v, want %v (LIFO teardown)", order, want)
		}
	}
}

// TestOnShutdown_JoinErrors proves every hook runs even when an earlier one
// fails, and that all failures surface via errors.Join.
func TestOnShutdown_JoinErrors(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	errBoom := errors.New("boom")
	errBang := errors.New("bang")
	var ran int
	app.OnShutdown(func(context.Context) error { ran++; return errBoom })
	app.OnShutdown(func(context.Context) error { ran++; return nil })
	app.OnShutdown(func(context.Context) error { ran++; return errBang })

	err := app.Shutdown()
	if ran != 3 {
		t.Fatalf("ran=%d, want 3 (a failing hook must not stop the others)", ran)
	}
	if !errors.Is(err, errBoom) || !errors.Is(err, errBang) {
		t.Fatalf("err=%v, want both boom and bang joined", err)
	}
}

// TestShutdown_Idempotent proves the once-guard: a second call — even through
// the other entry point — does not re-run hooks and returns nil.
func TestShutdown_Idempotent(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	var ran int
	app.OnShutdown(func(context.Context) error { ran++; return nil })

	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown #1: %v", err)
	}
	if err := app.ShutdownWithContext(context.Background()); err != nil {
		t.Fatalf("Shutdown #2: %v", err)
	}
	if ran != 1 {
		t.Fatalf("ran=%d, want 1 (hooks run once across both entry points)", ran)
	}
}

// TestShutdownWithContext_PropagatesCtx proves ShutdownWithContext passes its
// context — both values and deadline — through to every hook.
func TestShutdownWithContext_PropagatesCtx(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	type ctxKey string
	const key ctxKey = "k"
	deadline := time.Now().Add(30 * time.Second)
	ctx, cancel := context.WithDeadline(
		context.WithValue(context.Background(), key, "v"), deadline)
	defer cancel()

	var gotVal any
	var gotDeadline time.Time
	var gotOK bool
	app.OnShutdown(func(hctx context.Context) error {
		gotVal = hctx.Value(key)
		gotDeadline, gotOK = hctx.Deadline()
		return nil
	})

	if err := app.ShutdownWithContext(ctx); err != nil {
		t.Fatalf("ShutdownWithContext: %v", err)
	}
	if gotVal != "v" {
		t.Fatalf("hook ctx value=%v, want %q", gotVal, "v")
	}
	if !gotOK || !gotDeadline.Equal(deadline) {
		t.Fatalf("hook ctx deadline=%v ok=%v, want %v", gotDeadline, gotOK, deadline)
	}
}

// TestOnShutdown_AfterShutdownDropped proves registering after Shutdown has
// begun is a no-op: the hook never runs and nothing panics.
func TestOnShutdown_AfterShutdownDropped(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	var ran bool
	app.OnShutdown(func(context.Context) error { ran = true; return nil }) // must not panic

	// A subsequent Shutdown must not resurrect the late hook either.
	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown #2: %v", err)
	}
	if ran {
		t.Fatal("late-registered hook ran; want dropped")
	}
}

// TestOnShutdown_NilIgnored proves a nil hook is ignored at registration and
// never panics the drain loop.
func TestOnShutdown_NilIgnored(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.OnShutdown(nil)
	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestOnShutdown_ConcurrentRegistration proves registration is safe under
// concurrency (meaningful under -race): every hook registered before Shutdown
// runs exactly once.
func TestOnShutdown_ConcurrentRegistration(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	const n = 50
	var count int64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			app.OnShutdown(func(context.Context) error {
				atomic.AddInt64(&count, 1)
				return nil
			})
		}()
	}
	wg.Wait()

	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if count != n {
		t.Fatalf("count=%d, want %d (all concurrently-registered hooks run)", count, n)
	}
}
