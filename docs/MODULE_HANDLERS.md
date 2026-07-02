# Module handlers (HIP-0105)

zip mounts HIP-0105 extension modules as routes via `app.Module()`:

```go
app.Module("POST /v1/policy/eval",  "wazero",   "./ext/policy")
app.Module("POST /v1/transform",    "pyvm",     "./ext/transform")
app.Module("POST /v1/webhook",      "goja",     "./ext/webhook")  // recommended for multi-tenant JS
app.Module("POST /v1/route",        "starlark", "./ext/route")
```

Each `./ext/<name>` directory contains an `extension.json` manifest and
the compiled artifact (e.g. `validate.wasm` or `webhook.js`). See
[HIP-0105](https://github.com/hanzoai/hips) for the manifest spec and
the calling conventions per runtime.

## Wire format

zip builds one canonical JSON envelope per request and hands it to the
guest. Same shape regardless of runtime:

```json
{
  "method":    "POST",
  "path":      "/v1/policy/eval",
  "params":    {},
  "query":     {"q": "x"},
  "headers":   {"...": "..."},
  "body":      {"...arbitrary JSON..."},
  "org":       "hanzo",
  "user":      "z",
  "userEmail": "z@hanzo.ai"
}
```

The guest returns:

```json
{
  "status":  200,
  "headers": {"X-Foo": "bar"},
  "body":    {"...response..."}
}
```

If the guest returns a bare JSON value (not the envelope shape), zip
sends it as the response body with status 200.

## Native runtimes

Per HIP-0105 the runtime names are:

| Runtime | Languages | Sandbox | Notes |
|---|---|---|---|
| `native` | Go | none | compile-time linked; default for Hanzo-authored code |
| `goja` | JS | soft | recommended for multi-tenant JS at scale (~9 KB/module) |
| `wazero` | wasm | hard | language-agnostic (Rust/AS/TinyGo/Zig/C); pool default 4 |
| `pyvm` | Python | soft | full CPython incl. numpy/pandas; **single-tenant only** |
| `starlark` | starlark | soft | config DSL |
| `v8go` | JS | hard | **experimental — not production** (libv8 SIGSEGVs at scale) |

## AllowedRuntimes

For multi-tenant deployments, restrict which runtimes are accepted:

```go
app := zip.New(zip.Config{
    Loader:          loader,
    AllowedRuntimes: []string{"goja", "wazero"},  // no pyvm, no v8go, no native
})
```

`app.Module()` returns an error if the manifest's runtime is not in
the allowed list.

## Loader injection

zip does NOT depend on `hanzoai/base/plugins/extruntime`. The
`zip/runtime.Loader` interface is duck-typed:

```go
type Loader interface {
    LoadDir(ctx context.Context, dir string) (map[string]Module, error)
    LoadOne(ctx context.Context, dir string) (Module, error)
    Runtimes() []string
}
```

A real service binary constructs `*extruntime.Loader` from
`hanzoai/base/plugins/extruntime` with the runtimes it cares about
(goja + wazero + pyvm + starlark + native), and passes it in:

```go
loader := extruntime.NewLoader(
    nativevm.New(),
    gojavm.New(),
    wasmvm.New(),
    pyvm.New(),
)
app := zip.New(zip.Config{Loader: loader})
```

The duck-typed interface keeps zip lightweight (no transitive
hanzoai/base dep) while letting the unified Hanzo binary wire every
runtime once and share the loader across mounted subsystems.

## Lifecycle

`app.Module()` loads the module at registration time. The module is
closed on `app.Shutdown()` via an automatically-registered closer.
There's no per-request load / hot-reload today; if a real consumer
asks for it, the loader interface gains a `Watch()` method.
