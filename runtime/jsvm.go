// jsvm.go embeds a pure-Go JavaScript runtime (goja) into zip so a
// service can run TS/JS handlers in-process — no separate runtime
// service, no inter-service RPC, no container-per-service. Combined
// with esbuild.go (TS/modern-JS → ES5) this is zip's migration path:
// legacy TS source compiles to ES5 and drops straight into the embedded
// VM, then gets rewritten to a native Go handler in place over time.
//
// The pool pattern is lifted from hanzoai/base/plugins/gojavm and
// slimmed to what zip needs: each *goja.Runtime carries the host
// functions and modules registered at construction time, and requests
// borrow a free VM (or a one-off when the pool is saturated) so they
// never pay per-request VM creation cost.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dop251/goja"
)

// JSOptions configures a JSRuntime.
type JSOptions struct {
	// PoolSize is the number of pre-warmed *goja.Runtime kept hot. When
	// every pooled VM is busy, calls fall back to a freshly-built VM that
	// is discarded after the call. 0 selects a default of 8.
	PoolSize int

	// HostFns are Go functions exposed to JS as globals. Applied to every
	// VM in the pool (and to one-off VMs). Equivalent to calling
	// RegisterHostFn for each entry after construction, but applied to
	// the whole pool atomically at build time.
	HostFns map[string]any

	// Modules are CommonJS-style module sources registered into every VM,
	// reachable from JS via require(name). Applied at build time.
	Modules map[string]string
}

// JSRuntime is an embedded JavaScript runtime backed by a pool of goja
// VMs. It is safe for concurrent use: each call borrows an isolated VM.
type JSRuntime struct {
	pool *vmPool

	mu      sync.Mutex // guards the registration sets below
	hostFns map[string]any
	modules map[string]string
}

// NewJSRuntime builds a JSRuntime with the given options. Host functions
// and modules from opts are applied to every VM in the pool.
func NewJSRuntime(opts JSOptions) (*JSRuntime, error) {
	size := opts.PoolSize
	if size <= 0 {
		size = 8
	}
	rt := &JSRuntime{
		hostFns: map[string]any{},
		modules: map[string]string{},
	}
	for k, v := range opts.HostFns {
		rt.hostFns[k] = v
	}
	for k, v := range opts.Modules {
		rt.modules[k] = v
	}

	var buildErr error
	rt.pool = newVMPool(size, func() *goja.Runtime {
		vm := newVM()
		if err := rt.provision(vm); err != nil && buildErr == nil {
			buildErr = err
		}
		return vm
	})
	if buildErr != nil {
		return nil, buildErr
	}
	return rt, nil
}

// provision installs the registered host functions and modules into one
// VM. Called for every pooled VM at build time and for one-off VMs.
func (rt *JSRuntime) provision(vm *goja.Runtime) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for name, fn := range rt.hostFns {
		if err := vm.Set(name, fn); err != nil {
			return fmt.Errorf("zip/runtime: register host fn %q: %w", name, err)
		}
	}
	if err := installRequire(vm, rt.modules); err != nil {
		return err
	}
	return nil
}

// Eval evaluates src in a pooled VM and returns the exported Go value of
// the result (via goja's Export()). It is EvalContext with a background
// context — the evaluation cannot be cancelled.
func (rt *JSRuntime) Eval(src string) (any, error) {
	return rt.EvalContext(context.Background(), src)
}

// EvalContext evaluates src in a pooled VM under ctx and returns the
// exported Go value of the result. If ctx is cancelled or its deadline is
// exceeded before the evaluation completes, the running goja VM is
// interrupted and EvalContext returns ctx.Err() (context.Canceled or
// context.DeadlineExceeded). goja is single-threaded per borrowed VM, so
// a tight loop like `while(true){}` is preempted at the next interrupt
// check point rather than blocking the caller indefinitely.
func (rt *JSRuntime) EvalContext(ctx context.Context, src string) (any, error) {
	var out any
	err := rt.pool.run(func(vm *goja.Runtime) error {
		v, err := runUnderCtx(ctx, vm, func() (goja.Value, error) {
			return vm.RunString(src)
		})
		if err != nil {
			return err
		}
		if v != nil {
			out = v.Export()
		}
		return nil
	})
	return out, err
}

// runUnderCtx runs fn (a goja evaluation on vm) while a watcher goroutine
// interrupts vm if ctx finishes first. It always tears the watcher down
// and clears any pending interrupt before returning, so the VM is clean
// for the next borrower. When the interrupt fired, the goja
// *InterruptedError carrying ctx.Err() is unwrapped back to that error.
func runUnderCtx(ctx context.Context, vm *goja.Runtime, fn func() (goja.Value, error)) (goja.Value, error) {
	// Fast path: no cancellation possible, skip the watcher goroutine.
	if ctx.Done() == nil {
		return fn()
	}
	// Already cancelled before we start: don't even enter the VM.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	done := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-done:
		}
	}()

	v, err := fn()

	close(done)         // signal the watcher to stop
	<-watcherDone       // wait until it can no longer call Interrupt
	vm.ClearInterrupt() // drop any interrupt it set, clean for reuse

	if err != nil {
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if v, ok := ie.Value().(error); ok {
				return nil, v
			}
		}
		return nil, err
	}
	return v, nil
}

// InvokeFunc calls the global JS function named fnName with args (each
// converted to a goja value) and returns the exported Go value of its
// result. Like EvalContext, the call is interrupted and ctx.Err() is
// returned if ctx finishes before the function does. The named value must
// already be defined in the runtime (via Eval/EvalContext or LoadModule).
func (rt *JSRuntime) InvokeFunc(ctx context.Context, fnName string, args ...any) (any, error) {
	var out any
	err := rt.pool.run(func(vm *goja.Runtime) error {
		fn, ok := goja.AssertFunction(vm.Get(fnName))
		if !ok {
			return fmt.Errorf("zip/runtime: %q is not a callable JS function", fnName)
		}
		argv := make([]goja.Value, len(args))
		for i, a := range args {
			argv[i] = vm.ToValue(a)
		}
		v, err := runUnderCtx(ctx, vm, func() (goja.Value, error) {
			return fn(goja.Undefined(), argv...)
		})
		if err != nil {
			return err
		}
		if v != nil {
			out = v.Export()
		}
		return nil
	})
	return out, err
}

// RegisterHostFn exposes a Go function to JS as a global named name. The
// function is applied to every VM in the pool and to one-off VMs built
// afterwards. fn may be any value goja can bind (typically a Go func).
func (rt *JSRuntime) RegisterHostFn(name string, fn any) error {
	rt.mu.Lock()
	rt.hostFns[name] = fn
	rt.mu.Unlock()
	return rt.pool.forEach(func(vm *goja.Runtime) error {
		if err := vm.Set(name, fn); err != nil {
			return fmt.Errorf("zip/runtime: register host fn %q: %w", name, err)
		}
		return nil
	})
}

// LoadModule registers a CommonJS-style module under name. The module
// source is evaluated lazily the first time require(name) is called in a
// given VM. Applied to every pooled VM.
func (rt *JSRuntime) LoadModule(name, src string) error {
	rt.mu.Lock()
	rt.modules[name] = src
	rt.mu.Unlock()
	return rt.pool.forEach(func(vm *goja.Runtime) error {
		return registerModule(vm, name, src)
	})
}

// withVM borrows a VM from the pool for the duration of fn. Used by
// handler.go to invoke a loaded JS function with a request/response pair.
func (rt *JSRuntime) withVM(fn func(vm *goja.Runtime) error) error {
	return rt.pool.run(fn)
}

// Engine adapts this JSRuntime to the Runner's Engine interface so it can
// be registered as a language backend:
//
//	runner.Register("js", rt.Engine())
//
// The adapter is deliberately NOT Interruptible at the Runner seam:
// EvalContext already owns the per-call interrupt watcher and targets the
// exact pooled VM the call borrowed (interrupting only that VM, never a
// sibling concurrent call's VM). A Runner-level Interrupt would have to
// fan out to every pooled VM and could abort unrelated concurrent calls.
// So the goja engine self-manages cancellation inside Eval; the Runner's
// watcher is a no-op for it (and the registration warning is suppressed
// because Eval honors ctx fully). Engines without internal ctx handling
// implement Interruptible and let the Runner's watcher abort them.
func (rt *JSRuntime) Engine() Engine { return jsEngine{rt} }

// jsEngine is the goja Engine. Eval delegates to EvalContext, which runs
// the pool + joined-watcher path and fully honors ctx on the correct
// borrowed VM. args are ignored: a top-level expression's value is the
// result (use a trailing identifier to "return" a defined function).
type jsEngine struct{ rt *JSRuntime }

func (e jsEngine) Eval(ctx context.Context, src []byte, _ ...any) (any, error) {
	return e.rt.EvalContext(ctx, string(src))
}

// ctxHonored marks an Engine that fully manages ctx cancellation inside
// Eval, so the Runner skips the "not interruptible" warning and its
// watcher is a harmless no-op. jsEngine qualifies via EvalContext.
func (jsEngine) ctxHonored() {}

// newVM constructs a bare goja runtime with zip's field-name mapping
// (Go struct fields exported with their JSON tag names where present, so
// host objects look idiomatic from JS).
func newVM() *goja.Runtime {
	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	return vm
}
