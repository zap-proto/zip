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

`./...` does not reach `contract/` or `examples/local-service/`. Each has its own go.mod with a `replace` pointing at this checkout, so it tests the working tree. CI is `hanzo.yml`, run by `.hanzo/workflows/cicd.yml` through `hanzoai/ci`: vet, the root tests and both module checks.

`TestSDK_CallsTheLiveServiceOverZAP` and `TestProseReachesAServiceInPackageMain` compile programs offline against this checkout (`GOPROXY=off`, zip's go.sum) and skip under `-short`.

## Release

Patch tags, one above the highest `v*` on GitHub. Push the commit to main, read it back on `origin/main`, then push the tag, then check `curl -s https://proxy.golang.org/github.com/zap-proto/zip/@v/<tag>.info`. A published tag is never moved; a bad one is retracted in go.mod, as `retract v1.36.21` is.

## Map

| Files | Hold |
|---|---|
| `typed.go` | `Get`, `Post`, `Put`, `Patch`, `Delete`, the `With*` options, `registeredOp`. `invoke` decodes the body, binds declared headers, the query and the path, then `run` validates, authorizes and calls the handler. |
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

## Invariants

- One registry. `app.Registry()` is a projection of the walk over the app's entries, and every projection reads it and runs `op.invoke`. An untyped `app.Get(path, h)` has no op and appears in no projection.
- `opName(op)` is the operation's id on every surface: OpenAPI `operationId`, MCP tool name, call-plane name, CLI command. The default is `ID(method, path)`, which drops a leading `v1`.
- `hasBody(method)` is the one rule for which methods carry a JSON body. GET, HEAD and DELETE do not.
- A refusal is written by the error handler from the route the request matched: an RFC 9457 problem document (`application/problem+json`), or `{error, error_description}` at addresses declared with `OAuth`. `composeOAuth` builds that address set; `buildOpenAPI` reads the same set to pick each operation's `default` response (`problem-details` or `oauth-error`). Schemas in the document are objects, never booleans: `CommandsFromSpec` and other readers parse them as objects.
- The call-plane body is ZAP, laid out from struct field order (`internal/zapenc`). Append fields at the end. Reordering, inserting or retyping a field changes the wire.
- Doc prose is filed under `DocKey(pkg, method, path)`. zipdoc writes the package's import path. `pkgOf` reads the handler's package from its runtime name and maps `main` to the build info path, because a built binary names package main's functions `main.` while `go test` names them by import path.
- MCP over HTTP: POST only (GET and DELETE answer 405), one message per request, no sessions, no batches, notifications 202. `negotiate` answers `initialize` in the requested revision when it is 2025-06-18, 2025-11-25 or 2026-07-28, else in 2026-07-28. The official TypeScript (1.30.0) and Python (2.2.0) clients refuse an answer in a revision they did not request and do not know.
- zip validates no tokens. Identity is read from gateway-set headers (`HeaderOrg` is `X-Org-Id`); `Authorize` runs at the invoke seam, so it covers REST, MCP, the call plane and the in-process CLI.
- `app.Test` runs `prepare`, so `/mcp`, the OpenAPI document and the call plane answer under test as they do when serving.
- The `/docs` page loads Swagger UI from cdn.jsdelivr.net. Embedding swagger-ui-dist 5 would add about 1.74 MB (bundle 1,552,209 bytes, CSS 185,784 bytes, Apache-2.0) to every binary that links zip.

## Known issue

`zipdoc -check ./...` from a consumer's module root has reported files as stale that per-package runs call clean, and which files it reported varied between runs (seen in hanzoai/cloud). Run `-check` in each package directory, the way `go generate` runs it.
