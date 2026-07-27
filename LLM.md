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
| `app.Get/Post/...` | register a route |
| `app.Add(svcs...)` | compose units of functionality |
| `app.Listen(addrs...)` | serve here; the address scheme picks the transport |
| `app.Mount(prefix, addr)` | delegate there; same scheme registry, opposite direction |

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
`Service`. `zip.Load(prefix, Plugin)` returns one too — which is the whole
point: a linked-in service and a separately-built binary are the same type, so
where a service runs is a deployment decision, not a code change.

`Plugin` takes exactly one of `Addr` (already running), `Bin` (the binary,
normally `go:embed`'d), or `Path`. Non-`Addr` plugins are started as a child
process on a private unix socket in a 0700 dir.

**Reload invariants** — break any one and repeated reloads leak:

1. Routes register **once**, at `Load`, and resolve their target per request
   via `mountVia`. Re-registering on reload grows the route table without bound.
2. The replacement must be listening **before** any request moves to it, so a
   failed start changes nothing and a bad build cannot take a route down.
3. The old process is killed, reaped **exactly once** (one `cmd.Wait` per
   process, its result on a channel), its pooled conns closed, its dir removed.

`Unload` leaves routes answering 503 rather than removing them — same reason.

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
