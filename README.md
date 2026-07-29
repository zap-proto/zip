# zip

> **Docs:** [zip](https://zap-proto.dev/docs/zip) · part of the [ZAP Protocol](https://zap-proto.io)

The ZAP-native Go web framework. Built on [**Fiber v3**](https://github.com/zap-proto/fiber) / fasthttp. A route is declared **once, as a typed operation**, and zip projects it into REST, OpenAPI, MCP, a CLI and a by-name call plane — with **ZAP as the primary transport**, HTTP a secondary view of the same routes.

[**zap-proto.io**](https://zap-proto.io) · [Docs](https://zap-proto.dev/docs/zip) · [fiber](https://github.com/zap-proto/fiber) · [Spec](https://github.com/zap-proto/spec)

**ONE framework. ONE `Listen` verb. Operations declared once, served over every transport, projected into every interface.**

```go
package main

import (
    "context"
    "log"

    "github.com/zap-proto/zip"
    "github.com/zap-proto/zip/middleware"
)

// The In and Out types ARE the contract. Nothing below is written twice.
type GetUserIn struct {
    ID string `json:"id"` // binds from the :id path segment
}

type User struct {
    ID  string `json:"id"`
    Org string `json:"org"`
}

// GetUser returns one user, scoped to the caller's org.
func getUser(ctx context.Context, in *GetUserIn) (*User, error) {
    return &User{ID: in.ID, Org: zip.CallerOf(ctx).Org}, nil // gateway-minted identity
}

func main() {
    app := zip.New(zip.Config{})
    app.Use(middleware.Recover(), middleware.RequestID())

    // ONE typed op → a REST route, an OpenAPI operation, an MCP tool,
    // a `<service> <operation>` command, and a target for zip.Call.
    zip.Get(app, "/v1/users/:id", getUser)

    log.Fatal(app.Listen(":9653", "http://:8080")) // ZAP primary + HTTP extra, one verb
}
```

## Install

```bash
go get github.com/zap-proto/zip
```

Module path `github.com/zap-proto/zip`. Requires Go 1.26+.

## Features

- **Typed ops are the API** — `zip.Get/Post/Put/Patch/Delete[In, Out](on, path, fn)` is how a route is declared. It registers ONE operation; every interface below is derived from it, and `op.invoke` (decode → validate → authorize → run) is the one handler core all of them share. The In/Out types are the contract, so the document, the tool, the command and the client cannot drift from the code — there is nowhere for them to drift *to*.

  `on` is the App, any `Group` of it, or `app.With(mw…)` — a group's prefix is part of the op and `With`'s middleware wraps its handler, so structuring the router costs nothing in schema:

  ```go
  v1 := app.Group("/v1")
  zip.Get(v1, "/users/:id", getUser)               // one op at /v1/users/:id
  zip.Post(app.With(RateLimit), "/v1/keys", mint)  // gated, and still one op
  ```
- **Untyped routes are the escape hatch, and they cost every projection** — `app.Get(path, func(c *zip.Ctx) error)` registers a route and **no operation**. The endpoint is then in no OpenAPI document, is no MCP tool, has no command, and no service can reach it with `zip.Call`; it is reachable only by someone who already knows the URL. That is the right trade when the response is something a schema cannot describe — an SSE stream, a protocol upgrade, a proxied byte range, a non-JSON body — and the wrong one for everything else. If you can name what goes in and what comes out, declare it.
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
- **One registry, five projections** — one `zip.Get[In, Out](app, path, fn)` becomes a REST route, an OpenAPI 3.1 doc (`/.well-known/openapi.json`, Swagger UI at `/docs`), a Model Context Protocol tool at `/mcp` (JSON-RPC 2.0), a command line (`app.CLI()`, or `zip.CommandsFromSpec` for a client that links none of the service), **and** a by-name call plane other services reach with `zip.Call`. Same schema, same handler, one operation id addressing all five. Because each is an ordinary route, they ride every transport `Listen` was given — ZAP-native MCP is automatic. On by default; `Config.MCP.Disabled` to suppress.

  ```
                        ┌── REST route          method + path
                        ├── OpenAPI 3.1         operationId
  zip.Get[In,Out] ──op──┼── MCP tool            operationId
                        ├── CLI command         operationId
                        └── zip.Call plane      operationId
  ```
- **Services call services without linking** — `zip.DialApp("flags")` opens a ZAP connection over that app's canonical unix socket (`$ZIP_RUNTIME_DIR/flags.sock`) and `zip.Call[In, Out](ctx, c, "flags_bool", &in)` invokes one op, typed both ways, with the callee's `*HTTPError` intact on failure. No import of the callee's package, no hand-written client, no generated one to drift. Identity is the gateway's headers forwarded (`c.Forward()`) plus the kernel's `SO_PEERCRED` view of the calling process (`zip.PeerOf(ctx)`) — nothing the caller can forge.
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

Start with [`examples/hello`](./examples/hello) (two typed ops, four projections) and [`examples/zap-typed`](./examples/zap-typed). [`examples/sse-streaming`](./examples/sse-streaming) and [`examples/websocket`](./examples/websocket) are the two that stay untyped, and each says why at the top of the file — a stream and an upgrade have no single value for an `Out` to be. The `migrate-from-*` examples show the port as two steps: mechanical first, typed second, because stopping after the first leaves you with exactly the surface you were migrating away from.

## License

MIT — see [LICENSE](./LICENSE).
