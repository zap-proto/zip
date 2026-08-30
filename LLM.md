# zip — agent notes

`github.com/zap-proto/zip`. The Go web framework everything in the fleet serves
on. Built on the `zap-proto/fiber` fork over fasthttp. ZAP is the primary
transport; HTTP is a secondary view of the same routes.

**Canonical checkout: `~/work/zap/zip`.** Both impostors are retired.
`~/work/hanzo/zap-zip` was a stale duplicate clone of this same module and has
been deleted; every ref it held is on `origin`, so `git clone` restores it if
anyone ever needs it. `~/work/hanzo/zip` is `github.com/hanzoai/zip`, a dead
fork, now carrying a `DEPRECATED.md` pointing here — nothing should import it.

## Upgrading to v1.18.0 — read this before bumping

Four changes are visible from outside. Everything else is additive.

1. **A typed `Delete` no longer reads a request body.** Its input is the URL:
   path params and query string, which is what the document always said and what
   every generated client already sent. **Check every `zip.Delete` op whose In
   type has a field that is NOT a path param** — those fields used to arrive in
   a body and now must arrive as `?field=value`. A DELETE with only path-param
   fields is unaffected. (GET/HEAD were already URL-only.)
2. **`zip/zaprpc` is deleted.** Nothing in the estate imported it. If you have
   `zaprpc "github.com/zap-proto/go/rpc"`, that is a DIFFERENT module and is
   untouched — see below.
3. **The document says more.** A URL-borne field's `validate:"required"` now
   reaches its parameter, and a bodyless op's example now rides its parameters.
   Regenerated SDKs will make those arguments required — which they already were
   at run time.
4. **`zip.Get` and friends take an `OpTarget`, not a `*App`.** `zip.Get(app, …)`
   is unchanged; `zip.Get(v1Group, …)` now also works. `Router` embeds
   `OpTarget`, so **a Router implemented outside zip must add one method**:

   ```go
   func (m *myRouter) OpScope() zip.OpScope {
       s := m.inner.OpScope()
       s.Middleware = zip.Chain(s.Middleware, m.gate) // if you gate routes, GATE OPS
       return s
   }
   ```

   Embed the wrapped Router and override this; the embedded one answers for
   everything else. Known implementor: `hanzoai/commerce/middleware.Mint`.

A validation failure now names the field by its WIRE name (`reason`), not its Go
name (`Reason`) — if anything matched on those strings, it matched on the wrong
one.

## The verbs

Everything composes through these. There is deliberately no second way to do
any of them.

| verb | what it does |
|---|---|
| **`zip.Get/Post[In,Out](on, …)`** | **declare a typed op — THE way to declare a route.** The schema, and what every projection is derived from. `on` is the App or any Router of it |
| `zip.WithStatus(201)` | declare the SUCCESS status; it keys the document's response, not just the wire |
| `zip.WithResponse(302)` | declare a NON-2xx the op also answers — a 3xx it redirects with (`return nil, &zip.Redirect{Location: …}`), a 4xx/5xx it refuses with |
| `zip.WithRawBody("application/pdf")` | declare the body OPAQUE: nothing decodes it and the handler reads the exact bytes with `zip.BodyOf(ctx)` |
| `zip.WithFault[Body](409)` | declare a refusal WITH ITS SHAPE; the handler answers it with `zip.ErrConflict(…).WithBody(Body{…})` and that body is the whole response |
| `app.Add(svcs...)` | compose units of functionality |
| `app.Listen(addrs...)` | serve here; the address scheme picks the transport |
| `app.Mount(prefix, addr)` | delegate there; same scheme registry, opposite direction |
| `zip.Call[In,Out](ctx, conn, op, in)` | invoke an op on another app; `zip.Dial`/`DialApp` gets the conn |
| `app.Get/Post/...` | the ESCAPE HATCH: an untyped route. No op, no schema, invisible to every projection |

`zip.Get[In,Out]` and `zip.Call[In,Out]` are functions rather than methods only
because Go methods cannot take type parameters. Declaration and invocation
therefore read alike, and both take their subject first.

**The untyped verb is last in that table on purpose.** `app.Get(path, fn)` is a
route and nothing else. Reach for it only when the response is something a schema
cannot describe — an SSE stream, a protocol upgrade, a proxied byte range, a
non-JSON body — because a typed op is one decode, one call, one serialize and
genuinely cannot express those. `examples/sse-streaming` and `examples/websocket`
are the two exemptions in the tree and each states its reason in the file header.
Everything else is a typed op. If you can name what goes in and what comes out,
declare it: that declaration IS the API.

**A typed op declares on a Group as readily as on the App** (v1.18.0). `zip.Get`
and friends take an `OpTarget` — `*App`, any `Router`, or `app.With(mw…)` — so a
group's prefix is part of the op's path, and `With`'s middleware composes around
its handler:

```go
v1 := app.Group("/v1")
zip.Get(v1, "/users/:id", getUser)             // op path: /v1/users/:id
zip.Post(app.With(RateLimit), "/v1/keys", mint) // gated, and still one op
```

Until then `zip.Get` took the `*App` only, so a group-structured app had to
spell its prefix out per route to have typed ops at all — friction pushing
exactly the wrong way, since the untyped handler inherited the prefix for free.
zip composes the op's path with the router's own rule (`joinPath` mirrors
fiber's `getGroupPath`) and registers the composed path, so op.Path IS the route:
one string, no second composition to drift.

**`OpScope` is EXPORTED, and it has to be.** `Router` embeds `OpTarget`, so a
Router implemented outside zip must implement it — and a DECORATING router must
implement it faithfully. `hanzoai/commerce`'s `middleware.Mint` is the case that
proves it: it wraps a Router and prepends a platform-only gate to every route it
registers. A sealed (unexported) method would have made Mint uncompilable, and a
Mint that merely delegated would register a typed money-mint op with NO GATE —
its entire purpose, silently skipped. The shape is: embed the wrapped Router,
override `OpScope`, and fold your middleware into `s.Middleware`.

## One registry, five projections

`typed.go`. Every `zip.Get/Post[In,Out]` appends one `*registeredOp` to
`app.ops` — appended in exactly ONE place (`registerTyped`), and that slice is
the app's schema. Nothing else is authored; every interface below is derived
from it, and `op.invoke` (decode → validate → authorize → run) is the ONE
handler core all five share, so they cannot diverge in behavior.

| projection | file | consumer | addressed by |
|---|---|---|---|
| REST routes | `typed.go` | browsers, the external edge | method + path |
| OpenAPI 3.1 | `openapi.go` | humans, `/docs`, the published SDKs | `operationId` |
| MCP tools | `mcp.go` | agents | tool name = `operationId` |
| CLI commands | `cli.go`, `clispec.go` | operators, scripts | `operationId` → `<service> <operation>` |
| op-call plane | `call.go` | **other services** | `operationId` |

`opName(op)` is the one place the id rule lives; all five agree on the token.
Since v1.17.8 `Command.OperationID` carries it too, and `WithOperationID`
renames the command as well as the document, the tool and the call target —
before that the CLI spelled its own name from the route and one op had two
names. `App.OpenAPISpec()`, `App.MCPTools()` and `App.Commands()` read their
projection directly, with no transport in the way.

An **untyped** `app.Get(path, func(c *zip.Ctx) error)` registers no op, so it
appears in none of the five. That is the single biggest source of surface that
"exists" but cannot be documented, called by an agent, driven from a script, or
reached by another service. Fleet-wide it is also the majority of the surface,
which is what the typing migration is for: converting a route to
`zip.Get[In,Out]` is what turns four dark interfaces on, and nothing else does.

`app.Module()` (HIP-0105 extension routes) is the one remaining registrar that
cannot register an op; `module.go`'s doc comment states both structural reasons
and the one thing that would close it (an extension declaring its contract on
`runtime.Module`).

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

**The body is ZAP, and only ZAP (v1.18.5).** `internal/zapenc` derives a wire
layout from the In/Out type itself — fields take slots in declaration order,
each aligned to its own width, which is the rule zap's own schema builder
applies. Nothing is named on the wire: a field IS its offset. So a
service-to-service call carries the same bytes both ends hold in memory, with no
text format in the middle. JSON is the BOUNDARY encoding and stays on the REST
routes a browser reaches and in the MCP envelope an agent reads; it has no place
inside the binary protocol. Refusals cross as ZAP too (`callFault`), status
intact, so `errors.As` still recovers the `*HTTPError` the callee returned.

`op.invoke` takes the decoder as a PARAMETER, so there is still exactly ONE
handler core under REST, MCP, CLI and the call plane — the encoding belongs to
the transport, not to the contract.

**The compatibility rule follows from the layout: reordering, inserting or
retyping a field changes the wire. Append at the end, and only at the end.**

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
  forwards nothing, so an unattributed call looks unattributed. A background
  caller that legitimately acts FOR a tenant says so with
  `zip.WithCaller(ctx, zip.Caller{Org: …})`; an inbound request always wins over
  what it stated, so it can supply an identity but never launder one.

**The forwarded set is the WHOLE assertion (v1.18.4).** `identityHeaders` was
five and is now nine: org, **project**, user, **name**, email, **owner**,
isAdmin, **isOrgAdmin**, request-id. The four that were missing are the four a
callee decides on — a billing subject prefers the minted `X-User-Name` over the
opaque id, and platform sudo is read off `X-User-Owner` (membership of a
reserved org), never off `isOrgAdmin`, which says only "administers their own
org". Forwarding a subset meant the same handler decided differently depending
on whether it was reached over REST or over a call: `owner` arrived empty and
`isOrgAdmin` false, silently, in the direction that bills or admits the wrong
principal. `TestForwardedIdentityCrossesWhole` fails on exactly those four if
the array is ever narrowed again.
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

## The status is part of the contract (v1.18.2)

`WithStatus(code)` declares the status a successful op answers with — 201 for an
op that creates, 202 for one that accepts work it has not finished. Without it an
op answers 200, or 204 when the handler returns a nil Out.

It is an OpOption rather than something a handler sets per request because the
status is a CONTRACT detail, and the contract is what the registry projects.
`op.Status` keys the document's `responses` object, so a generated SDK expects
the status the service actually sends. A handler that reached around the
framework to set 201 wrote that detail into a side channel no projection could
read: the document said 200, every SDK said 200, and the wire said 201. That is
the same failure as a query parameter's required-ness being invisible — a
contract detail that only exists at run time is not a contract.

It is HTTP-only by construction: the REST route and the document read it, and the
MCP tool, the CLI command and the op-call plane are untouched, because each of
those carries its own outcome. A non-2xx panics at declaration — an error status
is the error a handler returns, and two places to say it is one too many.

### The answers that are not the success answer

`response.go`. WithStatus stays 2xx-only. A non-2xx is declared as what it is —
one more RESPONSE on the op — and SENT by what the handler returns:

| answer | declare | send |
|---|---|---|
| a location | `zip.WithResponse(302)` | `return nil, &zip.Redirect{Location: url}` |
| a refusal the contract names | `zip.WithResponse(409)` | `return nil, zip.ErrConflict(…)` |

`WithResponse(code)` adds the code to the document's `responses`: a 3xx with the
`Location` HEADER it answers with and NO body — that header *is* its answer — and
a 4xx/5xx with the envelope `errorHandler` writes, described from the `HTTPError`
type that writes it so the document cannot drift from the wire. Declaring says
what an op CAN answer; the wire still sends what the handler returned. A 2xx
panics: `WithStatus` already says that, and two options free to disagree is the
split this closes.

`*zip.Redirect` is the ONE thing on the typed path that can set a header, which
is why a redirect could not be typed at all before it — every OAuth leg that
hands a browser to its provider had to stay an untyped route, invisible to all
five projections. It rides the error channel because `(*Out, error)` is the whole
of what a typed handler can say, and `errorHandler` recognizes it BEFORE every
fault branch: status + `Location`, no body. Each projection then reads that one
value in its own vocabulary — the op-call plane and `zip.Call` report the
redirect's OWN status (302, not a collapsed 500, via `Unwrap`); an MCP
`tools/call` has neither status nor header, so the location is the RESULT rather
than `isError`, which would tell a model a working OAuth start had failed; the
CLI reports the same `*zip.Redirect` whether it ran the op in-process or over the
wire. A redirect with no location is refused as the bug it is (500) instead of
sent as a 3xx nothing can follow.

The nil-Out 204 is untouched.

### A refusal with a SHAPE

`fault.go`. A refusal is an answer, and an answer has a body. zip's is the
envelope — `{status, code, error}` — and it stays exactly that for every op that
declares nothing. `WithFault[Body](status)` declares the shape a refusal carries,
and `HTTPError.WithBody(v)` answers with it:

| answer | declare | send |
|---|---|---|
| a refusal with a shape | `zip.WithFault[Blocked](409)` | `return nil, zip.ErrConflict("step 2 is blocked").WithBody(Blocked{Step: 2, …})` |
| a refusal with no shape | `zip.WithFault[any](409)` | `return nil, zip.ErrConflict(…)` |

The declared body is the WHOLE body, not a field of the envelope — a service
whose published error contract is `{"error":{"code","message"}}` says exactly
that, at 402 and at 503, rather than having zip wrap it in a second error object.
That is why the shape cannot live in the type: one shape serves several statuses.
Before it, a route with a fixed error contract could not be typed at all: its
structure had to be flattened into the envelope's one string, and the document
said nothing about the status.

It projects everywhere a refusal is read. The document publishes the shape under
that status beside the success response (`$ref` into `components.schemas`), so a
generated SDK builds a typed error instead of reading a deliberate 409 as an
unexpected one. `HTTPError.MarshalJSON` is what writes it — on the VALUE, not at
the boundary, because a service under an outer error filter writes its own status
in band (`c.JSON(he.Status, he)`) and a body only zip's renderer honored would
vanish on those routes. The op-call plane carries the bytes in an APPENDED
`callFault` field, so a sibling service's `errors.As` sees the same shape a
browser reads (an older callee sends none; bytes that are not JSON are not
adopted). `Error()` carries it too, which is how it reaches a log, an MCP
`tools/call` result and the CLI. A 2xx or 3xx panics at declaration, and so does
one status declared twice — a response carries one schema.

Declaring is the SET of refusals; the error a handler returns is the ONE this
request met, which is why both name the status.

## The schema derivation — one type, one definition (v1.17.8)

`openapi.go`. `schemaOf(t, reg, fields)` is the ONE derivation from a Go type to
JSON Schema. Everything that needs to know what a value is reads it: the
document's request/response schemas, its query and **path** parameter types, the
MCP tool's `inputSchema`, and the CLI's flag kind.

**`schemaRegistry` is where a named struct is described once.** Every use of it
is a `$ref`; the projection supplies the map and the JSON Pointer prefix that
reaches it (`#/components/schemas/` in a document, `#/$defs/` in a standalone
schema). The entry is **claimed before the type's fields are walked**, and that
claim is the cycle guard.

Before it, the `registry` parameter existed and was never written to. Two
consequences, one cosmetic and one fatal:

- no `$ref` sharing — a type reached by ten ops was inlined ten times;
- **no cycle guard** — `type Node struct { Children []Node }` recursed forever.
  A self-referential In type overflowed the stack while building the spec AND
  while listing the MCP tools. A Go stack overflow is not recoverable, so the
  service could not start. `schema_test.go` pins both the self-referential and
  the mutually-recursive case; before the fix those tests did not fail, they
  killed the test binary.

`rootSchemaOf` is the standalone form MCP needs: an `inputSchema` has to BE the
object rather than a pointer at one, so the root is inlined and anything else it
reaches travels alongside in `$defs`.

**What this changed on the wire.** A tool whose In reaches no other named struct
is byte-identical to before — `$defs` is dropped when nothing refers to it, which
is every tool that existed before this change (a recursive one could not be
served at all). A tool whose In has a nested named struct now carries
`{"$ref": "#/$defs/User"}` plus a `$defs` block instead of the type inlined at
each use. That is standard JSON Schema 2020-12 and it is deliberate: it keeps the
tool list describing the same definition the document does, and it stops a type
used five times from being sent five times in a payload agents receive on every
conversation. Deliberate, and worth knowing if you consume `MCPTools()` by
walking `properties` without resolving `$ref`.

Two packages may both call a type `Config`. The registry qualifies the second
with its package (`pkg.Config`) instead of letting it overwrite the first; before
that both ops' `$ref`s pointed at whichever arrived last.

`flagType` (`cli.go`) reads `schemaOf` and maps the answer with `specType` — the
SAME schema-type → flag-kind rule the spec-derived CLI uses — rather than
re-deriving the vocabulary from `reflect`. A flag spelled from a Go type and the
same flag spelled from that type's published schema are equal by construction.

`urlFields` is the one list of URL-bindable fields, read by the path parameter
declarations, the query parameter declarations AND the CLI's flags for a
bodyless op — because `bindURL` is one binder over both halves of the URL, and
those are exactly the fields it can fill. Path params used to be hardcoded
`"type":"string"` while query params consulted the input type; the CLI offered
flags the URL could not carry.

`jsonFieldName` is the wire-name rule, and `validate.go` reads it too (v1.18.0):
a failed validation names the field the way the CALLER named it. It used to
report the Go name, so someone who sent `reason` was told `field "Reason"` is
required — a name appearing nowhere in the document they were working from.

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
over ZAP frames. As of v1.18.0 zip holds no second implementation of any of
them.

### `zaprpc/` — DELETED in v1.18.0, and never what cloud imported

Settled, so nobody has to work it out again: **`github.com/zap-proto/zip/zaprpc`
and `github.com/zap-proto/go/rpc` were different packages in different modules.**
cloud's internal service-to-service plane imports the LATTER, under the local
alias `zaprpc` — `zaprpc "github.com/zap-proto/go/rpc"`, in `rpc.go`, `dial.go`
and `zapface/*`. That alias is the whole reason the two got confused, and it is
why the deletion touches cloud not at all.

| | `zip/zaprpc` (deleted) | `zap-proto/go/rpc` (what cloud uses) |
|---|---|---|
| request | `Envelope{Service, Method string, Payload []byte}` | `Call{Method, PromiseID, Target uint32, Cap, Payload}` |
| header | 24 bytes | 28 request / 20 response |
| addressing | service+method **strings** | u32 **ordinals**, promise pipelining |

A peer speaking one could not decode the other, so `zip/zaprpc` was unreachable
by the rest of the ZAP fleet; nothing in the estate imported it, including zip.
Its docs cited `app.ZAPRegistry()`, a method that never existed in this module
(the only implementation was in the dead `hanzoai/zip` fork). To reach another
service use the op-call plane (`zip.DialApp` + `zip.Call`); for the ZAP wire
format use `zap-proto/go/rpc`, which owns it.

## Testing

`go test -race ./...`. The plugin tests build two genuinely different binaries
(`-ldflags -X main.version=`) from `internal/testplugin` and assert which one
answered, which is the only honest way to prove a reload swapped processes.

## A bodyless method, spelled once (v1.18.0 — WIRE CHANGE)

`hasBody` is the ONE rule about what a method carries on the wire, and it is now
the only spelling of it. It used to be documentation for a rule three other
sites re-stated differently, and DELETE had drifted:

| site | was | now |
|---|---|---|
| `openapi.go` `hasBody` | `GET, HEAD, DELETE` → no body | unchanged, and now the only definition |
| `typed.go` route handler | `method != "GET" && method != "HEAD"` → **read a DELETE body** | `hasBody(method)` |
| `cli.go` `Remote.Invoke` | same two methods → **sent a DELETE body** | `hasBody(c.Method)` |
| `cli.go` `bindIn` | every field became a flag | a bodyless op's flags are `urlFields` |

**What changed on the wire: a typed `Delete` no longer reads a JSON request
body.** Its input comes from the URL — the path params and the query string,
which is what every client generated from the document already sent. A service
that shipped a DELETE whose caller put fields in the body must move them to the
query string. Nothing else moves: GET/HEAD were already URL-only, and every
method with a body still has one.

It is about the WIRE, not about the op. `op.invoke` still decodes whatever JSON
it is handed for any method, because a call addressed BY NAME — MCP `tools/call`,
`zip.Call`, a CLI command — has no URL to carry half the input in. There the
arguments object is the whole input.

Two things a bodyless op used to lose in the document are closed with it:

1. **required-ness reaches the parameter.** `urlField.required` reads the same
   `validate:"required"` tag the body schema does, so a field the handler
   refuses to run without is required in the document. It used to be hardcoded
   `required: false`, so every generated client made the argument optional.
2. **the example survives.** A bodyless op has no `requestBody` for the doc
   comment's Example to live in, so it rides the parameters that carry its
   values (`exampleFields`), and `CommandsFromSpec` rebuilds the object from
   them (`specOp.example`). Every GET and DELETE reached the reference and the
   generated CLI with no example at all before this.

`TestCLI_SpecAndRegistryAgree` is now plain equality across every field of every
command for every method — no skip, no divergence helper. That test is the pin:
the registry-derived command tree and the document-derived one are one tree.

## A body that is bytes — `zip.WithRawBody` / `zip.BodyOf` (v1.18.7)

`op.invoke` decodes the body before the handler runs, which is what lets one
handler answer four codecs — and what made three kinds of route unwritable as
ops at all, so each stayed an untyped handler and out of all five projections:

| route | why a decoded In is not enough |
|---|---|
| Slack/GitHub HMAC, Discord Ed25519 webhooks | the signature is over THE BYTES; a struct decoded from the payload is not the payload, and re-encoding it signs to something else |
| `POST /v1/company/fundraise/deck` | the body is a PDF; there is no In to decode into |
| a hook that must ack an unreadable payload | decoding first makes it a 400, and a sender that retries every 4xx turns one malformed message into a storm |

`zip.WithRawBody(media...)` declares the body OPAQUE, and `zip.BodyOf(ctx)`
hands the handler the bytes this projection carried. Nothing else about the op
changes: path and query still bind onto In, `validate:` still runs, the
[Authorizer] still sees the value the handler will act on — for a raw op the URL
is the WHOLE of its structured input, which is why the document declares those
parameters even though the method has a body.

One declaration, read by every projection:

| projection | what it says |
|---|---|
| REST | `op.invoke` skips `dec`; the bytes reach the handler and an unparseable body is the handler's to answer for |
| OpenAPI | `requestBody` is the declared media with `{"type":"string","format":"binary"}` — bytes to a generator, a file picker in `/docs` — and no In schema, because the In is not the body |
| CLI | flags are the URL-bindable ones plus `--body <path>`, whose file goes out verbatim under the op's media; `CommandsFromSpec` reads the same fact back out of the document (a binary schema, not the media type — a webhook publishes `application/json` and still holds its bytes) |
| MCP + op-call | **absent.** An MCP tool's arguments are a JSON object by spec and the call plane's body is a ZAP message; neither is "the bytes a client sent", so `opByName` does not answer for a raw op and `mcpTools` does not list it. One predicate — `callableByName` — so the surface a model reads and the surface it can call are the same one |

Media types are sorted at declaration, so the registry and a document read back
off it name the same one of however many an op accepts. `WithRawBody` on a
method that carries no body panics at registration: there would be nothing to
read, and silently reading nothing is the failure it exists to remove.

The signature a webhook verifies rides a HEADER, and a typed handler declares
its inputs rather than reading the request — so that one header reaches it the
way anything else per-request does, from a middleware that puts it on the ctx
(`c.SetContext(context.WithValue(c.Context(), key{}, c.Header(…)))`). The BYTES
are the part only zip can supply, and that is what `BodyOf` is for. It is nil for
an op that did not declare a raw body: that op has its In decoded already, and a
second way to read one input is a second thing to keep true. The slice aliases
the transport's read buffer — copy it to keep it.

Fixed with it: `mcpCall` handed `op.invoke` the bare `fc.Context()`, so
`CallerOf` read back EMPTY for every `tools/call` while REST saw the gateway's
org — one op, two decisions about one caller, and an `Authorizer` keying on it
diverged per projection. It now passes `callerContext(fc)` like every other
caller of invoke.

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
