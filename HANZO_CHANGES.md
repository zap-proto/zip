# HANZO_CHANGES.md — diff from upstream zeekay/zip

`zeekay/zip` was a tiny `net/http` wrapper (~250 LOC) experimenting with
Sinatra-style routing in Go circa 2014. Hanzo adopted the name and
rebuilt the framework from scratch on **Fiber v3 / fasthttp** for the
canonical Hanzo Go web framework.

## What's gone

Every file from upstream has been deleted. We kept the name.

- `handlers.go` — old net/http handler registry. Replaced by
  `zip.App.Get/Post/...` plus internal `routerAdapter` over fiber.Router.
- `request.go`, `response.go` — old `Req` / `Res` types. Replaced by
  `*zip.Ctx` wrapping `fiber.Ctx`.
- `websocket.go` — old gorilla/websocket adapter. Replaced by package
  `wsx` over `fasthttp/websocket`.
- `examples/{hello,json,websocket}` — old examples. Replaced by 9 new
  examples covering Sinatra-style, typed handlers, OpenAPI, module
  routes, websocket, SSE, and three migration paths.
- Module path: `zeekay.io/zip` → `github.com/hanzoai/zip`.

## What's new

| Area | Implementation |
|---|---|
| Core | `*fiber.App` under the hood; `zip.App` is the public type. |
| Ctx | `fiber.Ctx` interface wrapped + `Org/User/UserEmail/IsAdmin/RequestID/Host` accessors. |
| Typed handlers | Generic `zip.Get[In, Out](app, path, fn)` etc. with reflection-build OpenAPI 3.1. |
| Validation | ~120 LOC reflection: `required`, `min/max`, `minlen/maxlen` struct tags. |
| OpenAPI | Auto-generated at `/.well-known/openapi.json`; Swagger UI at `/docs`. |
| Extension routes | `app.Module(method+path, runtime, dir)` — HIP-0105 surface. Loader is duck-typed (no hanzoai/base dep). |
| Middleware | `Recover`, `Logger`, `RequestID`, `Timeout`, `MaxBody`, `CORS`, `RateLimit`, `Telemetry`, `ProductionHeaders`. Auth + StripIdentityHeaders moved to hanzoai/gateway/middleware (HIP-0106). |
| Production headers | `ProductionHeaders(cfg)` stamps the Stripe/CF/GitHub-grade posture on every response (success/error/404): `Server` = white-label brand by Host (injected `Brand func(host) string`, never the framework name), `X-Api-Version` (brand-neutral), `X-Content-Type-Options: nosniff`, and `Strict-Transport-Security` when `HSTS`. X-Request-Id stays owned by `RequestID`. Never emits X-Powered-By or any framework/version string. |
| Adapters | `AdaptNetHTTP / AdaptNetHTTPFunc / AdaptNetHTTPMiddleware`; front a foreign subtree via `app.All(prefix+"/*", zip.AdaptNetHTTP(h))`. |
| WebSocket | `wsx.Upgrade(fn)` over fasthttp/websocket. |
| Streaming | `c.SendStream(reader)` + `c.SendStreamWriter(fn)`. |
| Transport | ONE verb `app.Listen(addrs...)`; the address scheme selects the transport (`:9653`=ZAP default, `http://:8080`=HTTP, any `RegisterTransport`'d proto). `app.Listen(":9653", "http://:8080")` serves both from one call. Routes ARE the surface over every transport. |
| Free MCP | Typed handlers auto-project to a Model Context Protocol tool surface at `/mcp` (JSON-RPC 2.0: initialize/tools/list/tools/call). Same op registry as OpenAPI (one schema, three projections: REST·OpenAPI·MCP), served over every transport → ZAP-native MCP for free. `Config.MCP.Disabled` to suppress. |
| Named-service RPC (optional) | `zaprpc.Service`/`Registry`/`Dispatch` + `zaprpc.HTTPHandler(reg)` for a gRPC-style named-service surface on top of the transport. |

## Dependencies

- `github.com/gofiber/fiber/v3 v3.2.0` — Fiber v3.
- `github.com/valyala/fasthttp v1.70.0` — transport.
- `github.com/fasthttp/websocket v1.5.12` — WebSocket.
- `github.com/luxfi/log v1.4.3` — canonical Hanzo logging. Forces
  Go 1.26.3 floor.

No `uber-go/zap`, no `log/slog`, no chi/gin/echo deps. Adapters use
Fiber's own `middleware/adaptor` package which is part of the v3 core.

## Policy adherence

- luxfi/log everywhere — zero `zap` or `slog` imports.
- No `.capnp` anywhere — ZAP only on the RPC plane.
- No major version bumps — all deps pinned to specific patches on
  existing major lines.
- luxfi canonical packages preferred — log was already on luxfi; no
  other Hanzo-canonical alternative existed for the Fiber surface.
- Go 1.26.3 floor — bumped from the brief's 1.23.6 only because
  luxfi/log canonical (the mandatory dep) requires it. Every other
  dep is on its current patch.
- HIP-0026 identity headers — `c.Org/User/UserEmail/IsAdmin` map
  directly to gateway-minted `X-Org-Id / X-User-Id / X-User-Email /
  X-User-IsAdmin`.
- HIP-0105 extension contract — duck-typed via `zip/runtime.Loader`
  so the framework stays decoupled from `hanzoai/base`.
- HIP-0106 Mount() contract — `examples/subsystem-mount` is the
  reference; the binary composes N subsystems via the same `Mount(app,
  deps) error` signature commerce/checkout uses today.

## Backwards compatibility

None. zeekay/zip and hanzoai/zip share only the name. The module path
changed (`zeekay.io/zip` → `github.com/hanzoai/zip`) so go-modules
treats them as distinct packages — no transitive collision is possible.
