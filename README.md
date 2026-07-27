# zip

> **Docs:** [zip](https://zap-proto.dev/docs/zip) · part of the [ZAP Protocol](https://zap-proto.io)

The ZAP-native Go web framework. Built on [**Fiber v3**](https://github.com/zap-proto/fiber) / fasthttp, with a terse route API, typed handlers that project to OpenAPI **and** MCP, and **ZAP as the primary transport** — HTTP is a secondary view of the same routes.

[**zap-proto.io**](https://zap-proto.io) · [Docs](https://zap-proto.dev/docs/zip) · [fiber](https://github.com/zap-proto/fiber) · [Spec](https://github.com/zap-proto/spec)

**ONE framework. ONE `Listen` verb. Routes defined once, served over every transport.**

```go
package main

import (
    "github.com/zap-proto/zip"
    "github.com/zap-proto/zip/middleware"
)

func main() {
    app := zip.New(zip.Config{})
    app.Use(middleware.Recover(), middleware.RequestID())

    app.Get("/health", func(c *zip.Ctx) error {
        return c.JSON(200, map[string]string{"status": "ok"})
    })

    v1 := app.Group("/v1")
    v1.Get("/users/:id", func(c *zip.Ctx) error {
        return c.JSON(200, map[string]string{
            "id":   c.Param("id"),
            "org":  c.Org(),  // gateway-minted X-Org-Id
            "user": c.User(), // gateway-minted X-User-Id
        })
    })

    _ = app.Listen(":9653", "http://:8080") // ZAP primary + HTTP extra, one verb
}
```

## Install

```bash
go get github.com/zap-proto/zip
```

Module path `github.com/zap-proto/zip`. Requires Go 1.26+.

## Features

- **Terse routes** — `app.Get(path, fn)` is the primary API; handlers are `func(c *zip.Ctx) error`.
- **Transport is a value, not a method** — one verb, `app.Listen(addrs...)`, and the address scheme selects the transport (mirrors `net.Listen`):

  ```go
  app.Listen(":9653")                  // ZAP (bare addr = the primary)
  app.Listen(":9653", "http://:8080")  // ZAP + HTTP in one call
  app.Listen("http://:8080")           // HTTP only
  app.Listen("/run/hanzo/app.sock")    // ZAP on a unix socket
  app.Listen("quic://:443")            // any RegisterTransport'd protocol
  ```

  ZAP (TLS 1.3 + post-quantum) is the default; HTTP is built in; `zip.RegisterTransport(scheme, zip.Transport{Serve, Dial})` adds any future protocol with zero change to `Listen` **or** `Mount`. The scheme names the *protocol* and the address names where it is spoken — a path is a unix socket, a `host:port` is TCP — so one wire never needs two schemes.
- **Composition is one verb** — `Service` is `func(*App) error`: a unit that attaches its own routes, middleware and shutdown hooks. A constructor taking dependencies and returning one is the same thing curried, so a composition root only ever sees `Service`.

  ```go
  app.Add(billing.New(deps), search.New(deps))
  ```
- **Plugins — a service builds as its own binary and loads at run time** — because a plugin is also just a `Service`, where it runs is a deployment decision rather than a code change:

  ```go
  //go:embed bin/billing
  var billingBin []byte

  app.Add(
      billing.New(deps),                                                     // linked in
      zip.Load("/v1/billing", zip.Plugin{Name: "billing", Bin: billingBin}), // its own binary
      zip.Load("/v1/ml",      zip.Plugin{Name: "ml", Addr: mlAddr}),         // already running
  )
  ```

  A plugin is an ordinary zip app — no SDK, no schema — started on its own unix socket and reached over ZAP. The host links zip and a transport, never a plugin's dependency graph, so its link time doesn't grow when a plugin does, plugins build in parallel, and `go:embed` keeps the deployment a single artifact. `app.Reload(name, bin)` swaps a running plugin for a new build without dropping a request: the replacement must be listening before any traffic moves to it, so a bad build can't take the route down, and routes register once and resolve their target per request, so repeated reloads stay flat in memory. `app.Mount(prefix, addr)` is the same delegation without the process management.
- **One registry, three projections** — `zip.Get[In, Out](app, path, fn)` registers one operation that becomes a REST route, an OpenAPI 3.1 doc (`/.well-known/openapi.json`, Swagger UI at `/docs`), **and** a Model Context Protocol tool at `/mcp` (JSON-RPC 2.0). Same schema, same handler. Because `/mcp` is an ordinary route, ZAP-native MCP is automatic. On by default; `Config.MCP.Disabled` to suppress.
- **Precedence is a property of the pattern** — routing comes from the [`zap-proto/fiber`](https://github.com/zap-proto/fiber) fork: the most specific pattern wins regardless of registration order (`static ≻ :param ≻ *`), and ambiguous equal-specificity overlaps panic at startup instead of silently shadowing.
- **Identity built-in** — `c.Org() / c.User() / c.UserEmail() / c.IsAdmin()` read JWT-validated `X-*` headers set by the gateway; handlers never parse tokens.
- **Middleware** — `Recover`, `RequestID`, `Logger`, `Timeout`, `MaxBody`, `CORS`, `RateLimit`, `Telemetry`, `Breaker` in `zip/middleware`.
- **WebSocket & streaming** — `wsx.Upgrade(fn)` over fasthttp/websocket; `c.SendStreamWriter` for SSE / chunked responses.
- **Extension routes** — `app.Module("POST /v1/eval", "wasm", "./policy")` mounts a sandboxed extension (wasm / goja / pyvm / starlark / v8go / native) as a route.
- **Embedded JS/TS runtime** — run a `(req, res)`-shaped JS/TS handler in-process via goja (pure Go, no CGO); esbuild transpiles TS ahead of it, for incremental migration to native Go.
- **Drop-in migration** — `app.All("/legacy/*", zip.AdaptNetHTTP(h))` fronts any `http.Handler` as one wildcard route; it obeys the same precedence, so a native route added later still wins.
- **Stdlib JSON only** — every JSON path goes through one internal helper backed by `encoding/json/v2` when built with `GOEXPERIMENT=jsonv2` (Go 1.25+), else `encoding/json`. No third-party JSON library.

## Documentation

The full guide — Ctx reference, the route-precedence contract, middleware, extension-runtime mounts, and versioning — is at **[zap-proto.dev/docs/zip](https://zap-proto.dev/docs/zip)**. Runnable examples live in [`examples/`](./examples).

## License

MIT — see [LICENSE](./LICENSE).
