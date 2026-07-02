// runner.go is zip's unified multi-language code runner. A Runner holds a
// registry of language → Engine and executes a source string against the
// engine registered for a language, each call in its own goroutine, bound
// to a context.Context. It generalizes the goroutine-scoped determinism
// contract of JSRuntime.EvalContext (one worker, one watcher, joined
// before return, engine-specific interrupt on cancel) across every
// language backend the host registers.
//
// DEPENDENCY DIRECTION. zip does NOT import hanzoai/base. The Engine
// interface here is a zip-side projection — duck-typed, exactly like the
// Loader/Module projection in package internal/runtime. base's backends
// (gojavm, pyvm, v8vm, wasmvm, starkvm) implement an extruntime.Runtime
// SPI; a thin adapter at the host's app-startup makes each satisfy this
// Engine and registers it. base imports zip and registers; zip exposes
// the registry and never names base. That is the whole point of the SPI:
// the registry is the seam, not an import edge.
//
// The one in-tree Engine zip ships is the goja engine — zip's own
// *JSRuntime, surfaced via (*JSRuntime).Engine(). It is pure Go, no cgo,
// and is the canonical "js" backend. Everything else plugs in from base.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// ErrUnknownLanguage is returned by Run when no engine is registered for
// the requested language.
var ErrUnknownLanguage = errors.New("zip/runtime: unknown language")

// Engine executes source in one language. It is the zip-side projection
// of base's extruntime backends; any value implementing Eval satisfies
// it, so base registers its backends without zip importing base.
//
// An Engine MAY additionally implement Interruptible. When it does, the
// Runner's watcher goroutine calls Interrupt(ctx.Err()) on cancellation
// so a runaway evaluation unwinds instead of leaking. When it does not,
// the watcher cannot abort a running call — the Run still returns
// ctx.Err() promptly, but the underlying worker runs to completion. The
// Runner logs a one-time warning per such engine at registration.
type Engine interface {
	// Eval evaluates src and returns the engine-native result value
	// (already converted to a Go value, e.g. goja's Export()). args are
	// passed through to engines that accept invocation arguments;
	// engines that only evaluate a top-level expression ignore them.
	//
	// Eval MUST respect ctx: at minimum it returns promptly once ctx is
	// done. Interruptible engines additionally honor Interrupt.
	Eval(ctx context.Context, src []byte, args ...any) (any, error)
}

// Interruptible is implemented by engines whose running evaluation can be
// aborted out-of-band (goja vm.Interrupt, pyvm PyErr_SetInterrupt, wasm
// trap). The Runner's watcher calls Interrupt with ctx.Err() on cancel.
type Interruptible interface {
	Interrupt(cause error)
}

// ctxHonorer is implemented by engines that fully manage ctx cancellation
// inside Eval (their Eval interrupts the correct resource itself). For
// these the Runner suppresses the "not interruptible" warning and its
// watcher is a benign no-op. zip's goja engine qualifies because
// EvalContext owns the per-call interrupt watcher.
type ctxHonorer interface {
	ctxHonored()
}

// Runner executes arbitrary-language code in goroutine-scoped, ctx-bound
// tasks. Safe for concurrent use: Run, Register and Languages may all be
// called from many goroutines.
type Runner interface {
	// Run looks up the engine for lang, spawns a worker goroutine that
	// calls engine.Eval, and a watcher goroutine that interrupts the
	// engine when ctx is done. It returns the engine result, or ctx.Err()
	// if cancellation won the race. Both goroutines are joined before Run
	// returns. If no engine is registered for lang it returns
	// ErrUnknownLanguage.
	Run(ctx context.Context, lang string, src []byte, args ...any) (any, error)

	// Register installs engine under lang. Re-registering a language
	// replaces the prior engine. Registration is concurrency-safe.
	Register(lang string, engine Engine) error

	// Languages returns the registered language names, sorted.
	Languages() []string
}

// NewRunner returns an empty Runner. Register backends before Run.
func NewRunner() Runner {
	return &runner{engines: map[string]Engine{}}
}

type runner struct {
	mu      sync.RWMutex
	engines map[string]Engine
}

func (r *runner) Register(lang string, engine Engine) error {
	if lang == "" {
		return fmt.Errorf("zip/runtime: Register: empty language")
	}
	if engine == nil {
		return fmt.Errorf("zip/runtime: Register %q: nil engine", lang)
	}
	_, interruptible := engine.(Interruptible)
	_, selfManaged := engine.(ctxHonorer)
	if !interruptible && !selfManaged {
		slog.Warn("zip/runtime: engine is not interruptible; ctx cancel will not abort a running call",
			"language", lang)
	}
	r.mu.Lock()
	r.engines[lang] = engine
	r.mu.Unlock()
	return nil
}

func (r *runner) Languages() []string {
	r.mu.RLock()
	langs := make([]string, 0, len(r.engines))
	for l := range r.engines {
		langs = append(langs, l)
	}
	r.mu.RUnlock()
	sort.Strings(langs)
	return langs
}

func (r *runner) Run(ctx context.Context, lang string, src []byte, args ...any) (any, error) {
	r.mu.RLock()
	engine, ok := r.engines[lang]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownLanguage, lang)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Goroutine-scoped task: one worker runs Eval, one watcher waits on
	// ctx.Done() and interrupts the engine. Both are joined before Run
	// returns so a borrowed backend resource is never left interrupted.
	// This is the EvalContext contract from jsvm.go, generalized.
	type result struct {
		val any
		err error
	}
	resCh := make(chan result, 1)
	done := make(chan struct{})
	watcherDone := make(chan struct{})

	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			if it, ok := engine.(Interruptible); ok {
				it.Interrupt(ctx.Err())
			}
		case <-done:
		}
	}()

	go func() {
		v, err := engine.Eval(ctx, src, args...)
		resCh <- result{v, err}
		close(done)
	}()

	res := <-resCh // worker finished (interrupted or naturally)
	<-watcherDone  // JOIN: watcher returned; no pending interrupt in flight

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return res.val, res.err
}
