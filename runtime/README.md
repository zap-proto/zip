# zip/runtime — embedded multi-language code runner

`runtime` embeds language backends into a zip service so a request can run
arbitrary-language source in-process — no separate runtime service, no
inter-service RPC, no container-per-language.

## Runner

`Runner` is a registry of `language → Engine` plus a goroutine-scoped,
`context.Context`-bound dispatch loop:

```go
type Runner interface {
    Run(ctx context.Context, lang string, src []byte, args ...any) (any, error)
    Register(lang string, engine Engine) error
    Languages() []string
}

func NewRunner() Runner
```

Each `Run` call:

1. Looks up the engine for `lang`. No engine → `ErrUnknownLanguage`.
2. Spawns a **worker** goroutine that calls `engine.Eval(ctx, src, args...)`.
3. Spawns a **watcher** goroutine on `ctx.Done()` that, for an
   `Interruptible` engine, calls `Interrupt(ctx.Err())` so a runaway eval
   unwinds instead of leaking.
4. **Joins both** goroutines before returning, so a borrowed VM is never
   handed back to its pool while still interrupted. Returns the engine
   result, or `ctx.Err()` if cancellation won the race.

This generalizes the settled `JSRuntime.EvalContext` contract (one worker,
one watcher, joined before return, engine-specific interrupt on cancel)
across every backend a host registers.

## Engine SPI

```go
// Implemented by every backend.
type Engine interface {
    Eval(ctx context.Context, src []byte, args ...any) (any, error)
}

// Optional: backend whose running eval can be aborted out-of-band
// (goja vm.Interrupt, pyvm PyErr_SetInterrupt, wasm trap).
type Interruptible interface {
    Interrupt(cause error)
}
```

An engine that fully manages cancellation inside `Eval` (zip's goja engine
does — `EvalContext` owns the per-call interrupt watcher and targets only
the borrowed VM) marks itself so the Runner's watcher is a benign no-op and
the not-interruptible warning is suppressed. An engine that is neither
`Interruptible` nor self-managed gets a one-time registration warning: its
`Run` still returns `ctx.Err()` promptly, but the worker runs to completion.

## Dependency direction — the registry is the seam, not an import edge

zip does **not** import `hanzoai/base`. `Engine` here is a zip-side
projection of base's `extruntime` backends; any value implementing `Eval`
satisfies it. base imports zip and registers its backends at app startup —
zip exposes the registry and never names base:

```go
// In the host binary (which already imports base) — NOT in zip:
runner := zipruntime.NewRunner()

// zip ships exactly one in-tree engine: pure-Go goja, no cgo.
js, _ := zipruntime.NewJSRuntime(zipruntime.JSOptions{PoolSize: 8})
runner.Register("js", js.Engine())

// base's backends plug in through the same seam via thin extruntime
// adapters — zip stays base-free:
runner.Register("py",    pyvmEngine())    // base/plugins/pyvm   (cgo CPython)
runner.Register("js-v8", v8vmEngine())    // base/plugins/v8vm   (cgo V8)
runner.Register("wasm",  wasmvmEngine())  // base/plugins/wasmvm (pure-Go wazero)
runner.Register("stark", starkvmEngine()) // base/plugins/starkvm
```

`gojavm` (`base/plugins/gojavm`) is the canonical first backend: it wraps
this package's `*JSRuntime`, so there is exactly one goja engine in the
stack and base, cloud, and every other zip consumer share it.

## See also

- `jsvm.go` — pooled goja VMs + `EvalContext` cancellation contract.
- `esbuild.go` — TS/modern-JS → ES5 for the goja engine.
- `examples/express-in-zip/main.go` — `POST /runtime/:lang` mounts the
  Runner over HTTP.
