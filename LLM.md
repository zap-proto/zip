# zip: notes for agents

`github.com/zap-proto/zip` is a Go web framework on the `zap-proto/fiber` fork of Fiber v3 (fasthttp). One typed operation declaration is the REST route, the OpenAPI operation, the MCP tool, the CLI command, the call-plane target and the generated SDK method. ZAP is the default transport (`DefaultScheme = "zap"`); HTTP serves the same routes. README.md is the user guide. This file is for changing zip.

Checkout: `~/work/zap/zip` (`~/work/zap-proto` links to `~/work/zap`). `~/work/hanzo/zip` is `github.com/hanzoai/zip`, a separate, deprecated module.

## Commands

| Check | Command |
|---|---|
| vet | `go vet ./...` |
| tests, about 2.5 minutes | `go test -count=1 -timeout 10m ./...` |
| tests without the ones that compile programs | `go test -short ./...` |
| race | `go test -race ./...` |
| collector module | `cd contract && go test -count=1 ./...` |
| example module | `cd examples/local-service && go tool zipdoc -check && go test -count=1 ./...` |
| Rust, every crate | `cd rust && cargo test --workspace` (about 2 s once built) |
| Rust lint | `cd rust && cargo clippy --workspace --all-targets && cargo fmt --all -- --check` |
| Rust documents | `cd rust/examples/<info\|stream> && make gen` (needs Go: `zipc` is this checkout's `cmd/zipc`) |

`./...` does not reach `contract/`, `examples/local-service/` or `rust/`. Each has its own go.mod with a `replace` pointing at this checkout, so it tests the working tree. CI is `hanzo.yml`, run by `.hanzo/workflows/cicd.yml` through `hanzoai/ci`: vet, the root tests and both module checks.

`TestSDK_CallsTheLiveServiceOverZAP` and `TestProseReachesAServiceInPackageMain` compile programs offline against this checkout (`GOPROXY=off`, zip's go.sum) and skip under `-short`.

## Release

Patch tags, one above the highest `v*` on GitHub. Push the commit to main, read it back on `origin/main`, then push the tag, then check `curl -s https://proxy.golang.org/github.com/zap-proto/zip/@v/<tag>.info`. A published tag is never moved; a bad one is retracted in go.mod, as `retract v1.36.21` is.

## Map

| Files | Hold |
|---|---|
| `typed.go` | `Get`, `Post`, `Put`, `Patch`, `Delete`, the `With*` options, `registeredOp`. `invoke` decodes the body, binds declared headers, the query and the path, then `run` validates, authorizes and calls the handler. |
| `body.go`, `answer.go`, `oneof.go` | the request kinds (`Body` field, `Parser`, `form:`/`cookie:` fields, `File`, `Consumes`) and the answer kinds (`Body`, `Verbatim`, `Sse`, `Socket`, `Redirect`, `CookieCoder`, `Produces`, `Or` and the `OneOf()` union). `intakeOf` and `answerOf` read them off In and Out once; `writeAnswer` is the one place they reach the REST wire. |
| `openapi.go` | the document (`buildOpenAPI`), `schemaOf`, `ID`, `SpecPath`, `DocsPath`, the Swagger UI page, the `default` refusal response |
| `mcp.go` | `App.MCP` (the door as a `zapmcp.Handler`), the `POST /mcp` adapter, `negotiate`, `MCPConfig` |
| `cli.go`, `clispec.go` | `Commands`, `CLI`, `LocalInvoke`, `Remote`, `CommandsFromSpec` |
| `call.go`, `here.go`, `ask.go` | `Dial`, `DialApp`, `Call`, `SocketPath`; `Serving`, `Here`, `Ask` |
| `sdk.go`, `rust_*.go`, `cpp_*.go` | generated clients, MCP servers and CLIs |
| `zapschema.go`, `zapread.go`, `layout.go`, `internal/zapenc` | `ZAPSchema`, `ReadZAP`, `Layouts`; the reflective ZAP codec behind the call plane |
| `doc.go`, `cmd/zipdoc`, `internal/zipdoc` | `Describe`, `DocKey`, `Prose`; the doc comment generator |
| `problem.go`, `ctx.go` | how a refusal is written (RFC 9457, or RFC 6749 at `OAuth` addresses); `Ctx`, `HTTPError`, the `Err*` helpers |
| `authorize.go` | `Authorize`, `OnResult`, `Decision`, `Approval`, `HeldOf`, `Build` |
| `caller.go`, `tenant.go` | `Caller`, `CallerOf`, `WithCaller`, `ActingAs`, `PeerOf`, the `Header*` names |
| `declare.go` | `Declaration`, `Described`, `Undeclared`, `Declares` |
| `compose.go`, `walk.go`, `build.go`, `generation.go`, `host.go` | the program (`Use`, `Group`), the walk every projection reduces, generations, `Serve` and `Host` |
| `transport.go` | `Listen`, `RegisterTransport`, `NetworkOf` |
| `load.go`, `remote.go`, `status.go`, `idle.go` | plugins (`Load`, `Plugin`, `Reload`, `Unload`), `Proxy`, `Plugins`, `Evict` |
| `resolve.go`, `graphql.go`, `docs_gen.go` | `GraphQL`; `DocsMarkdown` |
| `middleware/`, `wsx/`, `js/` | middleware; WebSocket upgrades; goja handlers |
| `contract/` | collector contract tests, a module of its own |
| `examples/local-service/` | the README's service, a module of its own |
| `rust/zip`, `rust/zip-macros` | zip in Rust: `#[zip::ops]`, `#[derive(zip::Wire)]`, the hyper and ZAP doors. See Rust below. |
| `rust/examples/info`, `rust/examples/stream` | the conformance corpus in Rust; a streaming service (JSON, SSE, byte relay) and its socket tests |

## Invariants

- One registry. `app.Registry()` is a projection of the walk over the app's entries, and every projection reads it and runs `op.invoke`. An untyped `app.Get(path, h)` has no op and appears in no projection.
- `opName(op)` is the operation's id on every surface: OpenAPI `operationId`, MCP tool name, call-plane name, CLI command. The default is `ID(method, path)`, which drops a leading `v1`.
- `hasBody(method)` is the one rule for which methods carry a JSON body. GET, HEAD and DELETE do not.
- A refusal is written by the error handler from the route the request matched: an RFC 9457 problem document (`application/problem+json`), or `{error, error_description}` at addresses declared with `OAuth`. `composeOAuth` builds that address set; `buildOpenAPI` reads the same set to pick each operation's `default` response (`problem-details` or `oauth-error`). Schemas in the document are objects, never booleans: `CommandsFromSpec` and other readers parse them as objects.
- The call-plane body is ZAP, laid out from struct field order (`internal/zapenc`). Append fields at the end. Reordering, inserting or retyping a field changes the wire.
- A pointer to a scalar, string or `[]byte` crosses as `list<T>` of at most one element: nil is the empty list, a pointer to zero is one zero element. A pointer to a struct is its message in a `bytes` slot, present when the slot is not null. `zapenc`, the `Layouts` emitter and the Rust projector (`TypeRef.Opt`) all follow this. A `*[N]byte` stays inline, so a pointer to an all-zero array reads back nil. Lists decode in one pass (`List.EachBytes`) and grow from the entries present, never the claimed count.
- A plugin request holds its instance (`hold`, `instance.busy`) until the reply is written, a streamed body is closed (zap-proto/http closes it when the connection ends, v0.3.12+), or an upgrade's tunnel ends; an upgrade over ZAP is refused 501 because ZAP's server never runs a hijack, and a declined upgrade's reply is read as a reply, bounded by `declineWait`. `evict` raises `closing` before it reads `busy`; `claim` raises `busy` before it reads `closing`. Eviction never stops a busy instance and takes idle ones coldest first by when their last request ended, skipping one whose lock is held. A starter waits for room until its own deadline (`Plugin.Start` from when it arrived), then 503; the ceiling is exceeded only when no process may be stopped and no start is in flight. `lazy`/`idle`/`start` are copied off the spec at Load because the request path reads them unlocked.
- Stopping a child is `terminate`: SIGTERM, then `Plugin.Grace` (default 20 s) for it to exit on its own, then SIGKILL. Every stop goes through it (Shutdown, Reload, Unload, eviction, a failed start). A plugin's shutdown hook only signals (`retire`); the outermost `Shutdown` waits for every child at once (`awaitChildren`), so N children cost the longest grace, not the sum. That wait ignores ctx: the drain usually spends it, and a host that returned first would be torn down under its children. Step 1 of `shutdown` stops every listener, then drains the HTTP transports' connections (`drainer`, fasthttp `ShutdownWithContext`) until ctx ends, so a stream a client keeps reading cannot hold a bounded shutdown. A composed app's shutdown runs as its parent's hook with `nestedShutdown` on the context and leaves the wait to the parent.
- Doc prose is filed under `DocKey(pkg, method, path)`. zipdoc writes the package's import path. `pkgOf` reads the handler's package from its runtime name and maps `main` to the build info path, because a built binary names package main's functions `main.` while `go test` names them by import path.
- MCP over HTTP: POST only (GET and DELETE answer 405), one message per request, no sessions, no batches, notifications 202. `negotiate` answers `initialize` in the requested revision when it is 2025-06-18, 2025-11-25 or 2026-07-28, else in 2026-07-28. The official TypeScript (1.30.0) and Python (2.2.0) clients refuse an answer in a revision they did not request and do not know.
- `AdaptNetHTTP` carries an upgrade: a request with `Connection: Upgrade` runs the handler inside fasthttp's hijack, on a writer that is an `http.Hijacker`, so a net/http WebSocket server (coder/websocket, gorilla) writes its own 101 and owns the socket; a handler that declines answers an ordinary HTTP/1.1 reply. `wsx` stays the native way for a zip handler.
- A handler may keep anything it reads off a request. Both transports serve a connection from one reused `RequestCtx` (fasthttp's server; zap-proto/http's `serveConn`), so a view of it is rewritten in place by the connection's next request: in hanzo-inc/cloud a kept header handed one API-key caller another tenant's principal. `fiberConfig` sets fiber's `Immutable`, so `Get`, `Params`, `Query`, `Queries`, `Cookies`, `FormValue`, `Path`, `Host`, `Body` and the rest return copies, and with them every `Ctx` accessor and the typed invoke seam. fiber's query, form, header and cookie binders ignore `Immutable`, so `Ctx.BindQuery` and `Ctx.Bind` on a non-JSON body run `detach` over what they filled (JSON is jsonenc's, which copies). Still views, by design: `c.Fiber().Request()`, `RequestCtx()`, and `c.Fiber().Bind()` called directly. `keep_test.go` sends two requests down one connection over HTTP and ZAP and requires the first one's values to stand. Cost (`Benchmark_Request`, dgx): typed op 146 → 152 allocs, +2% bytes; untyped handler 57 → 64 allocs, +10% bytes; a no-op route 36 → 35.
- `AdaptNetHTTP` hands the handler a request that owns its strings (`own`): method, host, header names and values and the URL are copied out of fasthttp's pooled buffer, which the next request on the connection overwrites. A handler may keep a header past its return; the body stays a view, valid until the handler returns (an upgrade's body is copied, since its handler runs after the buffer is released).
- zip validates no tokens. Identity is read from gateway-set headers (`HeaderOrg` is `X-Org-Id`); `Authorize` runs at the invoke seam, so it covers REST, MCP, the call plane and the in-process CLI.
- `app.Test` runs `prepare`, so `/mcp`, the OpenAPI document and the call plane answer under test as they do when serving.
- The `/docs` page loads Swagger UI from cdn.jsdelivr.net. Embedding swagger-ui-dist 5 would add about 1.74 MB (bundle 1,552,209 bytes, CSS 185,784 bytes, Apache-2.0) to every binary that links zip.

- A manifest op may say `"stream": "sse"`, `"bytes"` or `"socket"` (the answer is written as produced, or the connection upgrades; `ProjectOpenAPI` publishes `text/event-stream` with `x-events`, binary media, or `101` with `x-socket`) and `"raw": true` (the request body is bytes, bound to no type; the Rust front end's). A stream beside an `out` means either. `Check` refuses a socket beside an `out` and a raw body beside an `in`.
- Every wire is a typed op. A request body that is not JSON is a `zip.Body` field (bytes and Content-Type as sent), a `Parser` In (decodes itself; the document keeps In's schema), or a form: `*zip.File` parts make multipart, and `form:` fields bind only when the op `Consumes` a form media type (otherwise the tags are another binder's and JSON binds as before). A `cookie:` field is an `in: cookie` parameter set by the request's cookie and nothing else: the body, an argument object and the URL never set it, and no schema lists it as a body field. An answer that is not one JSON value is a `Body` (bytes, filename, its own status and headers; a `Reader` streams chunked; no bytes and no Type sends no Content-Type), `Verbatim[T]` (a relay's bytes documented as T; the upstream's 4xx/5xx pass undeclared), `Sse[T]` (`Send(ctx, emit)` on its own goroutine; ctx ends with the stream), `Socket[M]` (GET only; the handshake is decided before the handler runs: 501 over ZAP, 426 without an upgrade or for another version, 400 without a key, 403 for a browser origin the op does not admit — its own, plus `zip.Origins(...)`, `"*"` for any), `Redirect` or a `CookieCoder`; a type embedding `Body` or `Redirect` is that answer, so one with a `Cookies` method sets them beside it. A JSON answer keeps fiber's `application/json; charset=utf-8` unless the op `Produces` another. Every declared status carries the Out's schema, except one a union's alternative claims with its value-receiver `StatusCode()`. A union is a type whose `OneOf()` result types are its alternatives; `Or[A,B]` is the stock one. Over MCP bytes are base64 (in) and tool content (out); bytes, a Reader and a stream (as JSON lines) are bounded at `toolBound` and a Reader or stream at `toolWait`, and a socket op is no tool (`_meta.refused`). 204, 304 and a redirecting op's 3xx carry no body in the document; any other declared status carries the Out and its example. The call plane writes an `Sse` as the REST door does, refuses a `Socket` before its handler, and carries a union that holds a value as the union. A header field is a CLI flag under its field's name, as before; there are no header flags. An op that takes the body itself (a `Body` field, a form, a `Parser`) reads its content through `content`: each Content-Encoding (every header line) undone and held to BodyLimit, 415 for a coding it does not undo or a chain of more than three, 413 past the limit, 400 for one that does not decode, all before the handler; a multipart form is parsed in memory from that content, a url-encoded one of 10000 or more `&` (more than 10000 pieces, fields or empty) is 400 before any is decoded, and a form keeps only the keys In binds. Over HTTP fasthttp has read a multipart body as a form before routing, so a `Body` field holds that form re-encoded (what `c.Body()` gives a raw handler), not the bytes as sent; ZAP carries them as sent. A JSON op still reads fiber's `Body` as it always has. An answer refused after its handler ran closes its `Reader`. The graph (`/.well-known/graph`) and `Here` carry only plain ops (`registeredOp.plain`: one JSON value in, one out); a `cookie:` field is never a graph argument, and `Here` refuses a socket op before its handler.

## Rust

`rust/` is a Cargo workspace: `zip` (runtime), `zip-macros` (the two macros), `examples/info` (the corpus, compared byte for byte with `corpus/info` by `conformance_test.go`) and `examples/stream`. It is not published to crates.io; a service takes it as a git dependency pinned by commit, under the key `zip`, since the macros expand to `::zip::` paths (a service that also needs crates.io's `zip` archive crate renames that one):

```toml
zip = { git = "https://github.com/zap-proto/zip", rev = "<commit>" }
```

An op is an `async fn` in an `#[zip::ops]` impl block, with a route attribute and a doc comment. It takes `&self`, optionally `cx: &zip::Cx`, and at most one input: a `Wire + Deserialize` type bound from body, query, path and declared headers, or `body: zip::Body`, the request body as it arrives. It answers `Result<T, zip::Error>`: a `Wire + Serialize` type (200 JSON), `()` (204), `zip::Sse` or `zip::Body`.

```rust
#[zip::ops(app = "chat", title = "Chat", version = "1.0.0")]
impl Chat {
    /// Complete streams a completion as server-sent events.
    #[post("/v1/chat/completions")]
    async fn complete(&self, cx: &zip::Cx, arg: &Request) -> Result<zip::Sse, zip::Error> {
        let key = cx.header("authorization").ok_or(zip::Error::new(401, "no key"))?;
        let usage = cx.trailer("x-usage")?;                 // declared now, set at the end
        let events = self.upstream(key, arg).await?         // impl Stream<Item = Result<Event, E>>
            .map(|chunk| chunk.map(|c| zip::Event::data(c)));
        Ok(zip::Sse::new(events).keep(Duration::from_secs(15)))
    }

    /// Relay passes bytes through, both ways, as they arrive.
    #[post("/v1/relay")]
    async fn relay(&self, cx: &zip::Cx, body: zip::Body) -> Result<zip::Body, zip::Error> {
        cx.set_header("content-type", "text/event-stream")?;
        Ok(body)
    }
}
```

Serve with `zip::listen(app, Some(":8000"), Some(":9653"), zip::signal(), grace).await`, or one door with `zip::http::serve(Arc<App>, TcpListener, shutdown, grace)` / `zip::zaphttp::serve(...)`. `rust/examples/stream/src/lib.rs` is the worked example and `tests/serve.rs` its socket tests.

- Names are serde's. `#[derive(zip::Wire)]` reads `#[serde(rename)]`, `rename_all`, `skip` and `with`; it refuses `flatten`, a rename that differs by direction, `#[zip(json = "name")]` on a field and a `Vec<u8>` without `#[serde(with = "zip::base64")]` (serde writes bytes as a number array; the document says base64). `#[zip(...)]` holds what serde cannot: `url`, `header`, `required`, `text`, and a type's own schema (`json = r#"{...}"#`). A `text` newtype gets its serde impls from the derive (Display and FromStr). `serde_json::Value` is described as `prim: any`. A field the body may omit is an `Option` or carries `#[serde(default)]`; serde refuses a missing one otherwise.
- `bind` reads the body straight into the type when no URL value, declared header or required field applies; otherwise through a `serde_json::Value` it merges them into. A body past `MAX_BODY` (32 MiB) is 413, by its Content-Length before any byte is read.
- A stream item is a `Result`. `Ok` is written and flushed as yielded; `Err` ends the answer without its terminator (HTTP/1.1 truncated chunked body, HTTP/2 reset, ZAP no end frame), since the status already went out. A producer that wants to report in words sends an event and ends.
- Client gone: hyper drops the response body and with it the op's stream. HTTP/1.1 sees it on the read side (hyper's mid-message EOF check, `half_close` off), so an idle producer is dropped too: 1 to 3 ms in `tests/serve.rs`. `Sse::keep` writes `:` comment lines in quiet intervals for proxies.
- Trailers: `cx.trailer(name)` declares (`Trailer:` in the head) and returns a handle the stream keeps; set values go after the last chunk. HTTP/2 always carries them. hyper sends HTTP/1.1 trailers only to a request carrying `TE: trailers`, and only declared names. A JSON answer with a declared trailer goes chunked so it can carry one. Over ZAP they ride the end frame's slot (zap-proto/http v0.3.12's Go client reads past them without surfacing them).
- `serve` stops accepting on `shutdown`, lets each connection finish what it is answering, and after `grace` drops the rest (their streams' producers with them). hyper's HTTP/1.1 header read timeout is on (a timer is set).
- HTTP/1.1 keep-alive and HTTP/2 over cleartext (prior knowledge) on one port (`hyper_util` auto). No TLS: the ingress terminates it.
- The ZAP door speaks zap-proto/http's frames: request 0x01, response 0x02, streamed head 0x03, data 0x04, end 0x05, each preceded by its length as a BIG-endian u32 (header blocks are little-endian). The codec is `rust/zip/src/zap.rs`, written by hand because no zapgen emits Rust (zap-proto/go's tests assert the `rust` backend fails); `zap::tests::frames_are_the_ones_go_writes` pins it to frames Go wrote. Before this, the Rust door wrote a little-endian prefix and its build ran `zapgen -lang rust`, so it never built. A Go `zap-proto/http` client reads this door's JSON, streams (chunk by chunk) and refusals.
- A handler that panics answers 500; the server lives.

Bench (dgx, shared: load 12, GPU at 96%; `examples/stream` release, server pinned to 4 Cortex-X925 cores, `bombardier` on 4 others, `POST /v1/echo` with `{"text":"hi"}`, 10 s): 64 connections 1,201,848 requests (120k/s, p50 288 µs, p99 1.78 ms); 256 connections 1,672,052 (167k/s, p50 0.86 ms, p99 32 ms).

## Known issue

`zipdoc -check ./...` from a consumer's module root has reported files as stale that per-package runs call clean, and which files it reported varied between runs (seen in hanzoai/cloud). Run `-check` in each package directory, the way `go generate` runs it.
