# zip

> **Docs:** [zip](https://zap-proto.dev/docs/zip) · part of the [ZAP Protocol](https://zap-proto.io)

The ZAP-native Go web framework. Built on [**Fiber v3**](https://github.com/zap-proto/fiber) / fasthttp, with a Sinatra-style API, typed handlers that project to OpenAPI **and** MCP, and **ZAP as the primary transport** — HTTP is a secondary view of the same routes.

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

- **Sinatra/Express idiom** — `app.Get(path, fn)` is the primary API; handlers are `func(c *zip.Ctx) error`.
- **Transport is a value, not a method** — one verb, `app.Listen(addrs...)`, and the address scheme selects the transport (mirrors `net.Listen`):

  ```go
  app.Listen(":9653")                  // ZAP (bare addr = the primary)
  app.Listen(":9653", "http://:8080")  // ZAP + HTTP in one call
  app.Listen("http://:8080")           // HTTP only
  app.Listen("quic://:443")            // any RegisterTransport'd protocol
  ```

  ZAP (TLS 1.3 + post-quantum) is the default; HTTP is built in; `zip.RegisterTransport(scheme, fn)` adds any future protocol with zero change to `Listen`.
- **One registry, three projections** — `zip.Get[In, Out](app, path, fn)` registers one operation that becomes a REST route, an OpenAPI 3.1 doc (`/.well-known/openapi.json`, Swagger UI at `/docs`), **and** a Model Context Protocol tool at `/mcp` (JSON-RPC 2.0). Same schema, same handler. Because `/mcp` is an ordinary route, ZAP-native MCP is automatic. On by default; `Config.MCP.Disabled` to suppress.
- **Precedence is a property of the pattern** — routing comes from the [`zap-proto/fiber`](https://github.com/zap-proto/fiber) fork: the most specific pattern wins regardless of registration order (`static ≻ :param ≻ *`), and ambiguous equal-specificity overlaps panic at startup instead of silently shadowing.
- **Identity built-in** — `c.Org() / c.User() / c.UserEmail() / c.IsAdmin()` read JWT-validated `X-*` headers set by the gateway; handlers never parse tokens.
- **Middleware** — `Recover`, `RequestID`, `Logger`, `Timeout`, `MaxBody`, `CORS`, `RateLimit`, `Telemetry`, `Breaker` in `zip/middleware`.
- **WebSocket & streaming** — `wsx.Upgrade(fn)` over fasthttp/websocket; `c.SendStreamWriter` for SSE / chunked responses.
- **Extension routes** — `app.Module("POST /v1/eval", "wasm", "./policy")` mounts a sandboxed extension (wasm / goja / pyvm / starlark / v8go / native) as a route.
- **Embedded JS/TS runtime** — run an Express-shaped JS/TS handler in-process via goja (pure Go, no CGO); esbuild transpiles TS ahead of it, for incremental migration to native Go.
- **Drop-in migration** — `app.Mount("/legacy", h)` and `zip.AdaptNetHTTP(...)` accept any `http.Handler` (chi, gin, beego, `net/http`).
- **Composition by Mount** — a service is a set of subsystem packages exposing `func Mount(app *zip.App, deps Deps) error`; a standalone binary and a fused multi-subsystem binary are the same code with a different selection.
- **Stdlib JSON only** — every JSON path goes through one internal helper backed by `encoding/json/v2` when built with `GOEXPERIMENT=jsonv2` (Go 1.25+), else `encoding/json`. No third-party JSON library.

## Documentation

The full guide — Ctx reference, the route-precedence contract, middleware, extension-runtime mounts, and versioning — is at **[zap-proto.dev/docs/zip](https://zap-proto.dev/docs/zip)**. Runnable examples live in [`examples/`](./examples).

## License

MIT — see [LICENSE](./LICENSE).
