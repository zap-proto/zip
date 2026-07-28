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
