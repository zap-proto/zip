# zip — agent notes

`github.com/zap-proto/zip`. The Go web framework everything in the fleet serves
on. Built on the `zap-proto/fiber` fork over fasthttp. ZAP is the primary
transport; HTTP is a secondary view of the same routes.

**Canonical checkout: `~/work/zap/zip`.** Both impostors are retired.
`~/work/hanzo/zap-zip` was a stale duplicate clone of this same module and has
been deleted; every ref it held is on `origin`, so `git clone` restores it if
anyone ever needs it. `~/work/hanzo/zip` is `github.com/hanzoai/zip`, a dead
fork, now carrying a `DEPRECATED.md` pointing here — nothing should import it.

## The four verbs

Everything composes through these. There is deliberately no second way to do
any of them.

| verb | what it does |
|---|---|
| `zip.Get/Post[In,Out](app, …)` | declare a typed **op** — the schema, and what every projection is derived from |
| `app.Get/Post/...` | register an untyped route (no schema; invisible to every projection) |
| `app.Add(svcs...)` | compose units of functionality |
| `app.Listen(addrs...)` | serve here; the address scheme picks the transport |
| `app.Mount(prefix, addr)` | delegate there; same scheme registry, opposite direction |
| `zip.Call[In,Out](ctx, conn, op, in)` | invoke an op on another app; `zip.Dial`/`DialApp` gets the conn |

`zip.Get[In,Out]` and `zip.Call[In,Out]` are functions rather than methods only
because Go methods cannot take type parameters. Declaration and invocation
therefore read alike, and both take their subject first.

## One registry, four projections

`typed.go`. Every `zip.Get/Post[In,Out]` appends one `*registeredOp` to
`app.ops` — appended in exactly ONE place (`registerTyped`), and that slice is
the app's schema. Nothing else is authored; every interface below is derived
from it, and `op.invoke` (decode → validate → authorize → run) is the ONE
handler core all four share, so they cannot diverge in behavior.

| projection | file | consumer | addressed by |
|---|---|---|---|
| REST routes | `typed.go` | browsers, the external edge | method + path |
| OpenAPI 3.1 | `openapi.go` | humans, `/docs`, the published SDKs | `operationId` |
| MCP tools | `mcp.go` | agents | tool name = `operationId` |
| op-call plane | `call.go` | **other services** | `operationId` |

`opName(op)` is the one place the id rule lives; all four agree on the token.
`App.OpenAPISpec()` and `App.MCPTools()` read their projection directly, with no
transport in the way.

An **untyped** `app.Get(path, func(c *zip.Ctx) error)` registers no op, so it
appears in none of the four. That is the single biggest source of surface that
"exists" but cannot be documented, called by an agent, or reached by another
service.

## Calling another service — `call.go`

The cure for the mega-binary. An aggregator that reaches another app by
importing its package drags in that app's whole dependency graph; one that
hand-writes a client copies the schema into a second place to drift. Neither is
needed — the op already has a stable name, so a caller addresses it by name over
the app's own transport:

```go
c, err := zip.DialApp("flags")                  // $ZIP_RUNTIME_DIR/flags.sock
out, err := zip.Call[BoolIn, BoolOut](ctx, c, "flags_bool", &in)
```

The plane is an ordinary route (`CallPath = "/.well-known/zip/op/"`), so it
rides every transport `Listen` was given and inherits the app's middleware,
error handler, validator and `Authorizer` — it is exposed exactly as much as the
REST routes and no more. The whole input arrives as the body: addressing by name
means there is no URL to carry half of it, the same way `tools/call` does it.

Errors cross whole. The wire form is what `errorHandler` writes for every route
(`{status, code, error}`), so `errors.As(err, &he)` on the caller's side sees the
`*HTTPError` the callee returned, status intact. A void op yields `(nil, nil)`.

**Generic, not generated — deliberately.** A generated per-op Go client would
put a second copy of the schema in the caller's repo on its own regeneration
schedule: the drift this plane exists to remove, one directory over, plus a
fourth client generator racing openapi-generator (which already produces the
published SDKs from the OpenAPI projection). `Call[In,Out]` is type-safe at the
call site against the types the callee declared, with nothing to regenerate. A
caller wanting them checked at compile time imports the op's In/Out — a
types-only package, none of the handler's dependencies.

### The ONE socket-path scheme

```
SocketPath(name) = RuntimeDir() + "/" + name + ".sock"

RuntimeDir():  $ZIP_RUNTIME_DIR          set explicitly — always wins
               $XDG_RUNTIME_DIR/zip      a dev box, no configuration needed
               /run/zip                  the system default
```

Both halves use it — a service serves at `zip.Addr(zip.SocketPath("flags"))`,
a caller reaches it with `zip.DialApp("flags")` — which is what keeps them from
drifting. No registry, no discovery service, no second spelling: the name IS the
address, and one environment variable moves the whole fleet. `socketIn(dir,
name)` states the shape once; the plugin loader names a child's private socket
with the same function.

`Listen` creates the directory `0700` if it is missing, so serving at the
canonical path works on a fresh host. An existing directory keeps its own mode:
a deployment needing a socket shared across users creates the directory itself
and points `ZIP_RUNTIME_DIR` at it.

## Who is calling — `caller.go`

Two questions, two authorities, neither answered by the caller:

- **WHO the request is for** — the gateway's assertion, in the `X-Org-Id` /
  `X-User-*` / `X-Request-Id` headers (named once, as `zip.Header*`).
  `c.Forward()` propagates them onto the next hop; `zip.CallerOf(ctx)` reads
  them inside a typed handler (an untyped one has `c.Org()` and friends). `Call`
  forwards only what the ctx already carries — a bare `context.Background()`
  forwards nothing, so an unattributed call looks unattributed.
- **WHAT is calling** — `zip.PeerOf(ctx)` / `c.Peer()` return the kernel's
  `SO_PEERCRED` reading of the peer process (pid/uid/gid) on a unix socket. The
  peer never sends it, so there is nothing to forge. `nil` means "this host
  cannot attest the caller" (tcp, or a non-Linux host) — fail closed on it;
  never read a zero `Peer` as "attested as root".

An argv flag, an env var or a shared secret would all be things a caller states
about itself. `SO_PEERCRED` is the one thing about a caller it does not get to
say — which is why a service-to-service call over a unix socket needs no
credential of its own.

**Cost, measured.** `callerContext` attaches the live request to the typed-op
context, so `Benchmark_TypedRoute/typed` went 28 → **29 allocs/op** (1298 →
1346 B/op; ns/op inside noise). That one `context.WithValue` is what makes both
reads possible; the alternative — copying five headers onto every typed request
to serve the few that forward them — costs more and helps fewer. The **untyped**
path is untouched at 27 allocs/op, and
`TestServePathAllocsAreChainDepthInvariant` still pins it at one heap value per
request. Do not "fix" the 29 by removing the attachment; you would be removing
`CallerOf` and `PeerOf`.

## Transport is a value

`transport.go`. One registry, `map[string]Transport`, where
`Transport{Serve, Dial}` is a scheme in both directions. `Listen` uses `Serve`,
`Mount` uses `Dial`; a scheme may implement one and the other reports which was
missing. `DefaultScheme = "zap"`, so a bare address is ZAP.

The scheme names the **protocol**; the address names where it is spoken.
`networkOf` reads unix-vs-tcp off the address shape (leading `/`, `./`, or `@`),
which is why there is no separate `zapuds` scheme — one wire, one scheme.

Adding a protocol is one `RegisterTransport` call and changes neither `Listen`
nor `Mount`.

## Services and plugins

`service.go`, `load.go`. `Service` is `func(*App) error`. A constructor taking
dependencies and returning one is that curried, so a composition root only sees
`Service`. `zip.Load(Plugin, prefixes...)` returns one too — which is the whole
point: a linked-in service and a separately-built binary are the same type, so
where a service runs is a deployment decision, not a code change.

`Plugin` takes exactly one of `Addr` (already running), `Bin` (the binary,
normally `go:embed`'d), `Path` (on disk), or `URL`+`Sum` (a release artifact).
Non-`Addr` plugins are started as a child process on a private unix socket in a
0700 dir.

**The release lane.** `URL` without `Sum` is refused — fetching code over a
network and running it is where a host becomes an ACE vector — and the digest is
verified BEFORE the file is made executable or given its final name, so a
substituted artifact is a missing plugin, never a wrong one. The digest is also
the cache key: a restart is offline and a rollback to bits this host already ran
is free. `Plugins()` reports `Source:"url"`, `Version:<sha256>`.

The artifacts come from the SAME pipeline as everything else — root `hanzo.yml`,
`binaries:` block, `hanzoai/ci` (see `examples/plugin/README.md`). zip's own
`hanzo.yml` publishes `examples/plugin/billing` per OS/arch on every tag as the
reference artifact, so any host can exercise its install path without building
anything:

```
https://github.com/zap-proto/zip/releases/download/<tag>/example-billing-<os>-<arch>
https://github.com/zap-proto/zip/releases/download/<tag>/binaries.json   # url + sha256
```

**Reload invariants** — break any one and repeated reloads leak:

1. Routes register **once**, at `Load`, and resolve their target per request
   via `mountVia`. Re-registering on reload grows the route table without bound.
2. The replacement must be listening **before** any request moves to it, so a
   failed start changes nothing and a bad build cannot take a route down.
3. The old process is killed, reaped **exactly once** (one `cmd.Wait` per
   process, its result on a channel), its pooled conns closed, its dir removed.

`Unload` leaves routes answering 503 rather than removing them — same reason.
503 also beats 404 on the wire: 404 says "no such API" and is cacheable, 503
says "exists, currently down" and is retryable. `PluginStatus.Disabled`
separates a deliberate stop from a crash, since both answer 503. Unload also
pins a LAZY plugin down — otherwise the next request through its prefix would
start it straight back up, and disable would not stick for the plugins a
many-service host actually runs.

`ReloadTo(name, Plugin{URL, Sum})` is `Reload` to a DIFFERENT artifact — the
version-pin and the rollback. Same three invariants (it is the same code path);
the digest is the cache key, so rolling back to bits this host already ran
touches no network. `Reload(name, bin)` is that call with a `Bin` set.

Go plugins (`-buildmode=plugin`) are not used and cannot be: they require cgo
(so they break the `CGO_ENABLED=0` scratch images), demand byte-identical
builds of every shared dependency, and can never be unmapped, so an in-process
plugin's code and goroutines are unreclaimable for the host's lifetime.

## Routing precedence

From the fiber fork: most specific pattern wins regardless of registration
order (`static ≻ :param ≻ *`), and equal-specificity overlaps panic at startup
rather than silently shadowing. A `Mount` is an **ordinary wildcard route**, so
a more specific route registered afterwards still wins — pinned by
`TestMount_StaticBeatsRemoteMount`.

## The doc comment reaches every prose surface (v1.17.6)

`cmd/zipdoc` lifts the handler's doc comment and its In/Out field comments into
`zip.Describe`, and `docFor` is the one accessor. Three surfaces read it:
`openapi.go` (spec `description` + parameter/schema prose), `cli.go` (command and
flag help), and — since v1.17.6 — `mcp.go` (tool `description`, and
`schemaOfDoc` so the `inputSchema` properties carry their field comments).

`mcpTools` used to read `op.Summary` alone. That is only set by an explicit
`WithSummary`, so any op documented the canonical way — a doc comment and no
`WithSummary` — projected into a tool with an **empty description over a schema
whose fields said nothing**. It was invisible because the spec looked right: the
same op carried hundreds of characters of prose in `openapi.json`. Measured on
hanzoai/cloud, all 164 tools had an empty description before the fix and all 164
have one after (324 documented fields). `WithSummary` remains the fallback, so an
op in a package `zipdoc` never ran over still names itself.

If you add another projection that shows prose, read `docFor` — do not add a
second prose field. (The op-call plane shows none: a service reads the schema,
not the sentence, so it consumes `opName` and `op.invoke` only.)

## What lives elsewhere

zip does not define a ZAP envelope, registry, dispatcher or listener.
`zap-proto/go` owns the wire format and `zap-proto/http` owns HTTP semantics
over ZAP frames. `zaprpc/` is a leftover parallel implementation of that
envelope (string service+method, incompatible with upstream's ordinals) and
should go.

`zap-proto/http` v0.3.0 carries headers as length-prefixed name/value pairs —
`[u32 count]` then `[u32 nameLen][name][u32 valueLen][value]`. It used to be a
JSON `map[string][]string` inside the ZAP frame, encoded by two implementations
kept byte-identical to each other. Decode is now 0 allocs/op because every name
and value is a subslice of the frame. That was a wire break: peers must agree
on the version, so v0.3.0 and v0.2.x cannot talk.

## Testing

`go test -race ./...`. The plugin tests build two genuinely different binaries
(`-ldflags -X main.version=`) from `internal/testplugin` and assert which one
answered, which is the only honest way to prove a reload swapped processes.

## Known bug — `hasBody` is the ONE rule spelled in four places, and DELETE has already drifted

`openapi.go`'s `hasBody` says it is "the ONE place that rule lives … two copies
would eventually disagree". They already do — nothing calls it but the document:

| site | spelling | DELETE carries |
|---|---|---|
| `openapi.go:223` `hasBody` | `GET, HEAD, DELETE` → no body | query params |
| `typed.go:222` | `method != "GET" && method != "HEAD"` | **a body** |
| `cli.go:403` | `c.Method == "GET" \|\| c.Method == "HEAD"` | **a body** |
| `cli_test.go:353` | skips `GET/HEAD/DELETE` | — (hides the divergence) |

So a typed `Delete` route reads a JSON body the OpenAPI document says does not
exist, and any SDK generated from that document sends query params instead.
Both happen to work today (`bindURL(query)` runs either way), which is why it
has gone unnoticed; the CLI's spec path and its in-process path disagree about
the same op.

The fix is to call `hasBody` from `typed.go` and `cli.go` and delete the
`DELETE` skip in `cli_test.go` — but it moves DELETE's input from the body to
the URL on the wire, so it belongs in a release where consumers can be checked,
not in a patch. Not fixed here deliberately.

## Known bug — zipdoc: module-load extraction diverges from package-load

`zipdoc -check ./...` from a consumer's module root can flag files as stale
that `zipdoc -check` run in the package's own directory (and `go generate`,
which writes the same bytes) call clean — and WHICH files it flags varies
between runs (observed 2→4 in hanzoai/cloud across consecutive runs; per-pkg
mode stable across 15 packages). Render() is fully sorted, so the divergence
is in extraction under whole-module packages.Load, not in emission.
Until fixed, gates must invoke -check per package directory — exactly the
load mode the //go:generate directive uses. hanzoai/cloud's `make test` does
this; copy that shape, not `-check ./...`.
