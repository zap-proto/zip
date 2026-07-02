package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// EvalContext must interrupt a tight infinite loop when the context
// deadline fires, returning context.DeadlineExceeded promptly rather than
// blocking forever.
func TestEvalContext_DeadlineInterruptsInfiniteLoop(t *testing.T) {
	rt, err := NewJSRuntime(JSOptions{PoolSize: 1})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = rt.EvalContext(ctx, "while(true){}")
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("interrupt took %v, want ~50ms (well under the loop timeout)", elapsed)
	}
	t.Logf("infinite loop interrupted after %v", elapsed)
}

// A successful evaluation under a live (uncancelled) context returns the
// value, not a context error, and leaves the VM reusable afterwards.
func TestEvalContext_SuccessReturnsValue(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 1})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	v, err := rt.EvalContext(ctx, "40 + 2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, ok := v.(int64); !ok || got != 42 {
		t.Fatalf("EvalContext = %v (%T), want int64(42)", v, v)
	}

	// VM must be clean for reuse (no lingering interrupt) — run again.
	v2, err := rt.EvalContext(ctx, `"ok"`)
	if err != nil {
		t.Fatalf("reuse failed: %v", err)
	}
	if v2 != "ok" {
		t.Fatalf("reuse = %v, want ok", v2)
	}
}

// An already-cancelled context returns its error without entering the VM.
func TestEvalContext_AlreadyCancelled(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 1})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rt.EvalContext(ctx, "1 + 1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// InvokeFunc calls a named global JS function with Go args under a
// context, and interrupts it on deadline just like EvalContext.
func TestInvokeFunc_CallsAndInterrupts(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 1})

	if _, err := rt.Eval(`var addPair = function(a, b){ return a + b }`); err != nil {
		t.Fatal(err)
	}
	v, err := rt.InvokeFunc(context.Background(), "addPair", 19, 23)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(int64); got != 42 {
		t.Fatalf("addPair(19,23) = %d, want 42", got)
	}

	if _, err := rt.Eval(`var spin = function(){ while(true){} }`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = rt.InvokeFunc(ctx, "spin")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("InvokeFunc(spin) err = %v, want DeadlineExceeded", err)
	}
	t.Logf("InvokeFunc(spin) interrupted after %v", time.Since(start))
}
