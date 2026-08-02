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
| `app.Add(svcs...)` | compose units of functionality |
| `app.Listen(addrs...)` | serve here; the address scheme picks the transport |
| `app.Mount(prefix, addr)` | delegate there; same scheme registry, opposite direction |
| `app.Graft(children...)` | **compose an APP in process** — the parent's router learns the child's shape, the child keeps its behaviour (v1.18.16) |
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
`zip.Module`).

## PROTOTYPE (branch `proto/one-verb-fold`) — one verb, one walk

**Not on main. Not tagged. Not released.** This section describes the branch
only; everything below it describes main and still holds there.

`compose.go`, `walk.go`, `build.go`, `remote.go`.

**The thesis.** `Graft` was a symptom; the EAGER registry was the disease. The
registry was a `[]*registeredOp` FIELD that composition appended to at call
time, so composing two apps meant merging two registries, and `AdaptNetHTTP`
type-erasing an `*App` into a closure destroyed five projections at once. Make
the registry a lazy PROJECTION over a walk and composition collapses to
appending a reference.

**The model.** An App is a PROGRAM: an ordered list of `entry{node, callsite}`
where the node is one of exactly three payloads — a `Handler` (middleware), a
`route`, or an `*App` **included by reference**. `Use` appends the first and the
third; the route methods append the second. `Group` returns a child `*App` with
a prefix, so "a scope" and "a sub-application" are one mechanism.

Not mount, not proxy, not delegate, not embed: **inclusion by reference**. One
interpreter walks one program and reads the child's entries in place. There is
no run-time boundary, so nothing hops. Those words are reserved for `Mount`,
the one case where another runtime genuinely exists.

**The walk is the contract.** `walk(app) ([]occurrence, error)` flattens the
program in preorder — one occurrence per entry visit, all three kinds, with
prefix, middleware stack and breadcrumb. Every projection is a REDUCER over that
slice: the router, the registry, OpenAPI, MCP, CLI, the call plane, the conflict
report, the lint. Guarantees: pure (no I/O), deterministic, append-stable
(composing C never reorders A's occurrences — what keeps generated SDKs
diff-stable), and validated before any reducer runs.

The AST and the walk are unexported. They are the contract projections are
written against, not a compatibility surface; the tests for them are in-package
for that reason.

**Snapshot semantics.** A node's middleware environment is the stack inherited
at its INCLUSION SITE plus the middleware preceding it at its own level. So
(a) parent entries written after an inclusion site do not reach that subtree,
and (b) entries written inside a subtree, whenever written, inherit the anchored
environment. Contents may grow until seal; environment may not. **This diverges
from Fiber**, where the stack is whatever had been registered by the time a
route was — migration note material.

Staged composition (middleware appended after an inclusion) is legal and means
what it says. It is intentional co-located and a latent bug written far apart,
and nothing can tell those apart, so it is `app.Lint()` — a report naming both
call sites — and not an error at any tier.

**Generations (replaces seal-forever).** A program is not mutable; it is
VERSIONED. A generation is a sealed program plus everything projected from it,
and the live system is one `atomic.Pointer` to the current one.

- **Build then swap.** A composition change builds N+1 from the current entry
  list ± the changed refs, runs the whole walk and every validation, and swaps
  only on success. A colliding plugin fails the build with breadcrumbs and **the
  old generation keeps serving** — load and reload are transactional, so a bad
  plugin cannot take down routing.
- **Requests are generation-pinned.** `Listen` serves a handler that loads the
  pointer once on arrival; an in-flight request completes on its own generation.
  Lock-free, no lock on the served path in any phase.
- **Verbs.** `Use` pre-build (a declaration, cannot fail, returns the receiver);
  `Include`/`Drop` against a live system (transactions, return an error, change
  nothing on failure). Two names because one can fail and the other cannot.
- **Freeze** happens at first appearance in a BUILT generation, propagates
  across the reachable graph, and is monotonic. Direct edits panic with a
  message that names both call sites and says to go through a generation.
  Reading never freezes.
- **`Drop` is receiver-local**, which answers the shared-definition question:
  an entry belongs to the app that wrote it, so a host drops only what the host
  included. `Drop(billing)` when a GROUP holds the reference is a no-op; drop
  the group. Anything else would let one host reach into a definition another
  host also serves.

**Occurrence ids.** One definition included twice declares ONE id and needs TWO
operations, or the document is invalid. Qualification is prefix-derived and
unconditional: `v1.listInvoices` vs `admin.listInvoices`. Never positional —
"first wins" and "append -2" both make generated output a function of mount
order, so reordering two lines in a wiring file becomes an SDK break. Surface
keys on the occurrence; TYPES key on the definition, so one `Invoice` stays one
schema.

**Mount is a leaf.** `Mount(prefix, addr, decl...)` appends an App like any
other inclusion. The declaration is a BUILD INPUT and is never fetched: a walk
that did I/O would make `Registry()` fallible and slow and make the document
depend on another process being up at boot. `Route.Op` (new, additive) is what
lets a declaration say which op answers which address.

### Acceptance: the iam graft case, byte for byte

Reproduced with `iamserver.NewApp(db)` (hanzoai/iam v1.34.0) grafted into a
4-path host, rendered through `OpenAPISpec()`:

| | released zip v1.18.22 | this branch |
|---|---|---|
| host alone | 4 paths | 4 paths |
| iam alone | 81 paths | 81 paths |
| after graft | **85 paths / 102 operations** | **85 paths / 102 operations** |
| document sha256 | `e0b1924352028050…` | `e0b1924352028050…` |

**Byte-for-byte identical.** iam v1.34.0 also compiles against this branch with
NO source change — it has no bare closure at a `Use` call, so it needs no
`zip.H`.

Note the real number is **4 → 85 paths** (98 iam ops + 4 host ops = 102
operations), not the 4 → 164 in circulation. 164 is not what this pairing
produces; it is presumably the whole api.hanzo.ai document across every
subsystem, or a different iam. The acceptance property — composition is the
union, and the prototype changes no byte of it — holds either way.

### Things the code proved, that the design did not predict

1. **Middleware placement is depth-dependent.** Root (`depth 0`) middleware
   stays router middleware — unchanged behaviour, and it still covers requests
   that match nothing (404 logging, CORS preflight). Middleware inside an
   INCLUDED definition is composed into that subtree's own route chains instead,
   because a pathless `app.Use(guard)` placed on the host's router is a barrier
   for the whole host binary — exactly the failure `Graft`'s delegation existed
   to avoid. Cost: an included definition's middleware no longer covers
   unmatched paths, which is arguably correct (a definition does not answer for
   addresses it does not declare) but IS a change.
2. **Binding a handler to an App is a BUILD step, not a registration step.**
   `toFiberHandler` was called at registration with the declaring app; one
   definition served by two apps then hands out a `*Ctx` belonging to an app
   that is not serving. Routes carry unbound `[]Handler` and bind at
   materialisation.
3. **zip's own control routes are NOT part of the program.** They are a
   projection OF it, so making them entries made rendering a projection a
   mutation (seal, then ask for the document, then panic) and made them
   inheritable by a host. They belong to the build.
4. **Materialising on a read is a write, and reads are concurrent.** Found by
   `-race`, not by reading. `router()` takes a mutex; `Registry()` keeps its
   `sync.Once`. The version counter is process-wide (an append to a child
   changes a parent's meaning), so a sealed app must stop consulting it or an
   unrelated App composing anything rebuilds this one's router.
5. **Errors the router raised at registration now surface at build.** Fiber's
   ambiguous-route panic (`/x/:id` vs `/x/:name`) fires at `Fiber()`/`Listen`,
   not at the second `app.Get`. Structural and not undoable: an App can be
   included after its routes are written, so at any single registration the set
   of patterns it must coexist with is not yet known.
6. **A no-op transaction still installs a generation.** The number is a build
   counter, not a change counter; detecting "nothing moved" would be a second
   code path for one operation.
7. **ZAP cannot carry a free-form value.** The tidy symmetry — forward a mounted
   op over the remote's own ZAP call plane — is impossible: ZAP encodes structs
   and this side has no struct, by construction. Mounted ops forward over the
   declared REST route with JSON, which is the boundary encoding anyway.

## Composing an app in process — `Graft` (v1.18.16)

`graft.go`. `app.Graft(child)` puts a live `*App` behind a host's router **and
keeps its registry**. It is the fifth verb, and the one that was missing:
`Listen` serves here, `Mount` delegates there, `Add` composes a registrar,
`Load` composes a binary, **`Graft` composes an app**.

**What it replaces, and why that was a hole.** The only prior way was
`app.All(prefix, zip.AdaptNetHTTP(childHandler))`. `AdaptNetHTTP` takes an
`http.Handler` and returns a closure (`adapt.go`, three lines) — so the App went
in and a bare function came out, and the child's `registry` went with it. All
five projections die at that closure: the document, the MCP tools, the CLI
commands, the call plane and the `Declaration`. The host could publish only the
wildcard it hung the closure on. hanzoai/cloud published **five path keys** for
an identity provider holding **94 typed ops**; the other 89 existed, were typed,
carried schema and prose, and reached no consumer.

`AdaptNetHTTP` is NOT dead — it is still the right tool for a genuinely foreign
handler (a mux, a gin engine, a reverse proxy). What died is `AdaptNetHTTP`
applied to a `*zip.App`.

**Delegation, not route-copying.** For each pattern the child declares, the
parent registers that pattern pointing at ONE handler that runs the child's own
router on the same fasthttp request. This is the load-bearing call:

- copying the child's route handlers drops its middleware — `Declaration`
  projects `GetRoutes(true)`, which filters `Use` entries out — and a service
  whose whole authentication seam is `app.Use(guard)` would arrive with the
  guard gone;
- copying its `Use` entries too is worse: a pathless `Use` grafted onto the
  parent gates **every route in the host binary**.

Delegation has neither failure, because middleware stays inside the app that
declared it. `graft_test.go`'s `TestGraft_MiddlewareStaysInsideTheChild` is
that proof, both directions.

It is also cheaper than what it replaces (no net/http round trip, so the
adapter's ~5% and its dropped fasthttp user-context both go) and it **narrows**
the surface: a wildcard swallows every unknown path under its prefix, while
Graft registers only what the child declares, so an undeclared path falls
through to the parent.

**Six decisions, each a consequence of that one.**

| | rule | why |
|---|---|---|
| paths | untouched | `op.Path` is already the whole absolute path; rewriting it names a route that does not exist |
| operationIds | untouched | an id is a published SDK method name; rewriting it at compose time makes an SDK method a function of where the app is deployed |
| registry | **copy**, with `Origin` | the parent's registry is its own value, so composing never edits what the child says about itself |
| authorizer | the CHILD's | `op.invoke` closes over `child.authorizer`; the parent never silently re-authorizes a child's op under its own rules |
| tags | the op's own, else `Origin` | a grafted product is a named group, not an untagged blob. Derived, never invented |
| schema names | `<origin>.<Type>`, **always** | see below |

**Schema names are qualified unconditionally, not on collision.** Qualifying only
on collision makes a published type name a function of *who else is in the
room*: add a schema to one app and another app's type silently renames, which
renames an argument type in every generated SDK. It is one extra input to
`schemaRegistry.nameFor`, which already qualified by package — not a second
naming scheme. Qualifiers compose outermost-first: origin, then package, then an
ordinal. An app with nothing grafted is byte-identical to before
(`TestGraft_UngraftedDocumentIsUnchanged`).

The collision is real, not hypothetical: iam and the cloud fleet each own an
`Application` (an OAuth client, 83 props / a hiring application, 16 props) and a
`Role` (14 props / 2 props). Unqualified, the weave refuses with
`Conflict{Kind:"schema"}` and the fleet document does not build.

**The refusal is all-or-nothing and reads the ROUTER, not the document.** Before
registering anything, Graft checks every `METHOD pattern` each child declares
against the parent and against its siblings; any overlap returns one error
naming both claimants, in `claim()`'s vocabulary, and registers nothing. It
reads `Declaration()` because the addresses that actually collide are liveness
paths no document publishes — `GET /healthz` is the one that already caused a
production outage when a co-mingled child answered it.

**No prefix argument.** A child's paths are its own — a real identity provider
owns three disjoint prefixes (`/v1/iam`, `/login/oauth`, `/.well-known`) and one
prefix string cannot say that. A host that wants a child elsewhere builds it on
a `Group` of that prefix; that is the prefixing mechanism that already exists.

**Lifecycle.** Graft must run before `Listen`: the document, the tool list and
the call plane are rendered once, from the registry, at `prepare()`. An op
grafted after that would serve and never describe — the exact defect Graft
exists to remove — so it refuses (`App.prepared`). It prepares the child
(idempotent), does **not** adopt the child's control plane (`Declaration`
excludes it, so the parent keeps its own `/docs`, MCP door and op plane), and
adopts the child's teardown so a graft cannot leak the child's store.

**When NOT to reach for it.** Graft is in-process composition of a `*zip.App`.
A child that is a separate service over the wire (a reverse proxy, an HTTP
client to another pod) has no `*App` here to graft; forcing one would mean
linking that service's whole dependency graph into the host, which is the cost
`Mount` exists to avoid. That case needs the mirror primitive — the host reads
the child's `/.well-known/openapi.json` + `/.well-known/zip/plugin.json` across
the wire at compose time, with the same all-or-nothing refusal. Not built.

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
that both ops' `$ref`s pointed at whichever arrived last. Since v1.18.16 the
same rule takes one more input — the app that DECLARED the op, when the op came
in on a `Graft` — and applies it unconditionally rather than on collision. See
the Graft section for why on-collision is the wrong trigger for a published
name.

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

---

# The Zip specification

Canonical. Two documents, one dependency rule: the runtime may cite the
language; the language does not cite the runtime. The code implements this.

# Zip: the language

Zip is a language for describing an HTTP application. An App is the
program. The walk is the interpreter. Everything else — serving, OpenAPI,
SDKs, CLI, MCP, llms.txt, loaders — is a backend.

One principle above the rest: **the process that composed the program is
the only authority on it.** Every backend is a reducer over the same tree.
There is no second tool because there is no second authority.

This document is complete without the runtime (zip-runtime.md) or any
loader (zip-deployment.md). If those change, this file does not.

## The program

    app := zip.New()
    app.Use(logging)                   // middleware: wraps what follows
    app.Get("/health", health)         // operation: answers an address

    v1 := app.Group("/v1")             // a child App with a prefix
    v1.Use(auth)
    v1.Use(users)                      // another App, composed in

    // hand the finished tree to a runtime: see zip-runtime.md

Three kinds of thing, and only three:

- **App** — an interior node. It has a prefix (possibly empty) and an
  ordered list of entries. An App may appear in two places: one
  definition, two occurrences.
- **Handler** — middleware. Wraps requests. Never answers one.
- **operation** — a leaf. Answers one address with one typed op.
  Registered by `Get`, `Post`, etc. Schemas come from `Op[In, Out]`.

Order is meaning. `Use(a); Use(b)` is a program, not a set.

**Constructors are not structure.** Root, group, plugin, remote, feature —
none of these is a node kind. Anything that returns an `*App` composes:
`Group(p)` is `New` with a prefix plus `Use`; a loader is a constructor
defined outside the language. The walk cannot tell a loaded App from a
written one. That is the extension point, and it is the whole extension
point.

## Composition API

| Name | Does |
|---|---|
| `New() *App` | an empty program. |
| `Use(...Component)` | appends middleware and Apps, in order. |
| `Group(prefix) *App` | creates and appends a child App. |
| `Get/Post/...(path, ...Handler)` | appends an operation. Closures work directly. |
| `zip.H(func(Ctx) error) Handler` | adapter, needed only for bare closures passed to Use. |
| `Component` | sealed: `Handler` and `*App`. Exported so `[]zip.Component` is writable; not implementable outside zip. |

## Representation

    type entry struct {
        n    node     // Handler | operation | *App
        site callSite // runtime.Caller at registration
    }

`node` is a sealed marker. One type switch exists, in the walk. Call sites
are mandatory: every error and lint finding points at file:line, and you
cannot add call sites to entries that never recorded them. App fields are
unexported; the entries slice is not a public surface.

## The walk

    walk(app *App) ([]occurrence, error)

One function, and the only code that inspects entry payloads as
`Handler | operation | *App`. Depth-first, in entry order, carrying
context: prefix path, middleware stack (copies share structure), origin
trail (`root → billing → /v1`). It returns the flattened result — one
occurrence per visit: (def, kind, ctx, site).

`occurrence` is unexported and non-semantic: the walk's internal
flattened result, not a public model, not a stored source of truth. The
tree remains canonical. The materialized form is deliberate: the
alternatives are rewalking once per reducer, buffering secretly (the
slice exists but the document lies), or rendering during traversal
(weakening validation-before-rendering). At real scale — a thousand-odd
routes — the slice is free.

**Binding requirement.** Every validator and reducer consumes the walk's
result; none reopens `App.entries` or recurses the tree. Downstream code
may switch on a normalized occurrence kind, never on payload types. A
package test enforces it mechanically: the only type switch over `node`
lives in walk.go.

Pipeline, in order, or nothing publishes:

    App tree
      → walk once
      → structural validation, complete (cycles, conflicts, inert middleware)
      → derived validation (ID derivation, post-derivation collisions)
      → reducers (router, registry, SDK, MCP, CLI, llms.txt)
      → publish — or write nothing

- **Occurrences, not definitions.** An App referenced twice appears
  twice, each with its own context. Definition identity is the pointer,
  valid because frozen definitions never change.
- **Pure.** No global state. Both validation stages complete, with
  trails, before any reducer runs.
- **Deterministic.** Same program, same slice, every time. This is why
  every generated artifact is reproducible, and why any two backends
  agree.
- **Append-stable.** Adding an entry never reorders occurrences inside
  previously composed subtrees. Order only: a new entry can still create
  a conflict that fails validation.

## Semantics

**Snapshot scope.** A thing sees the middleware written before it:

    app.Use(a)
    app.Use(pub)      // pub sees a
    app.Use(b)
    app.Use(priv)     // priv sees a, b

**Anchoring.** A subtree's environment is fixed where it was composed, not
where its contents were later written:

    v1 := app.Group("/v1")
    app.Use(auth)      // after the Group: does not reach v1
    v1.Get("/x", h)    // late is fine; sees v1's environment, without auth

This differs from Fiber, where registration time decides. The migration
note ships with the release.

**Middleware wraps; operations answer.** Doctrine, enforced by validation:
a Handler may not terminate a request. `Use(static)` is refused — register
it at its address: `app.Get("/assets/*", h)`. Use cannot say *where* a
handler answers; that is what operations are for. Middleware with nothing
beneath it in the subtree is refused, naming its call site.

**The diamond is legal.** One definition, two references, two occurrences,
two prefixes, possibly two environments. No backend may veto a composition
the language permits.

**IDs.** An operation's ID is the dot-joined prefix path plus its name:
`v1.billing.invoices.list`. Names default from method and path; the ID is
a function of position, never of composition order. After derivation, a
collision means two different definitions claiming one name — refused,
with both trails. Before derivation, the diamond colliding with itself is
expected and resolved by the rule. MCP tool names, SDK symbols, and CLI
commands all use this rule; agents and humans see one name everywhere.

**Descriptions.** Prose comes from the op declaration (a description
field, as in OpenAPI's summary) and lands in every backend alike. One
source. Prose written anywhere else about this API is the drift bug this
language exists to kill.

## Validation errors

Accumulate, with trails: conflicts, cycles, inert middleware,
post-derivation ID collisions — joined with `errors.Join`, each carrying
call sites. Never fail on the first. Suspicion that cannot be a rule
(middleware after an App on the same receiver: intentional co-located,
latent bug cross-scope, structurally identical) lints in vet; semantics
stay expressive.

## Removed, and why

Graft, Mount, Attach, and two more verbs: five verbs across four metaphor
domains compensated for registry mutation at compose time; the walk made
the registry a backend, and the verbs had nothing left to name.
Per-kind wrappers and the ref type: every wrapper held only a call site,
and every entry holds one, so one envelope replaced them all.
The Occurrence type as public artifact: the language exports no IR — no
Occurrence type, no stored model, no cached contract. The walk's
flattened result exists, unexported and non-semantic; what died was its
claim to be part of the language. The tree is canonical; the slice is
implementation.

The test behind every deletion: a concept may go if removing it loses no
semantic distinction the system depends on. Everything above survived it.

---

# Zip: the runtime

Depends on zip-language.md. The language does not depend on this.

A program describes an application; a **host** runs one:

    host := zip.Serve(app, ":8080")   // build, validate, freeze, serve

    host.Include(payments)            // live change: next generation
    host.Drop(users)
    host.Reload(billingV2)
    host.Close()

`Include`, `Drop`, and `Reload` are host verbs, not App verbs. `Use`
extends a program; `Include` publishes a new generation of a running one.
Different worlds, different receivers.

## Freeze

Definitions freeze at first inclusion in a built generation. Mutating a
frozen App panics at the call — the stack trace is the locality. `Use`
against a served tree panics for the same reason: a silently recorded
entry that activates on someone's later Include is the worst outcome, so
the hole does not exist.

**A shared definition is immutable everywhere.** Changing a shared
subsystem means building a new version and `Reload`ing it at each host
that wants it. There is no cross-host transaction, no process-level server
registry, no lock ordering — those were machinery for reaching into shared
subtrees from an App-anchored Include, and moving the verb to the host
deleted the reach. Hosts on different versions mid-rollout is ordinary
deployment reality, not a hazard this runtime papers over.

## Generations

The program is immutable per generation; a host is an atomic pointer to
the current one. Change means: build the next generation, validate
completely (the walk's first pass), swap only on success. A bad change
cannot take down routing — the build fails with trails and the old
generation keeps serving. Requests pin their generation on arrival; no
locks on the hot path; drained generations are collected. Each generation
retains its walk result; reducers serving the live generation — the
OpenAPI endpoint, the MCP list, llms.txt — reuse it rather than
rewalking.

## Inspection

`host.Registry()` reflects the live generation. Before any host exists,
`Registry(app)` recomputes per call and is not goroutine-safe — mutable
phase, caller's problem. Registry and Declaration panic with the joined
validation error when the program does not compose — validation runs
before any rendering, so no tool can emit an empty document and exit 0.
CLI entry points recover, print the errors, and exit 1. Panic is
transport there, never presentation.

## Removed, and why

The `building` exemption: protected no reachable legitimate path; its only
effect was admitting a Use that raced a build. Deleted; freeze is
unconditional.
The process-level live-server set and the multi-server transact clauses
(reachability filtering, creation-order locks, all-or-nothing cross-host
commitment): existed only because Include was anchored to Apps. Host
anchoring made the question they answered unaskable. The old guarantee's
own text conceded cutover was never simultaneous; explicit per-host
reloads are the same reality without the machinery.
