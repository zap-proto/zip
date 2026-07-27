# plugin

A service that builds as its own binary and loads into a host at run time.

```bash
go build -o bin/billing ./billing     # the plugin, built on its own
go build -o bin/host    ./host        # the host, which does not import it
(cd . && ./bin/host)

curl localhost:8080/health
curl localhost:8080/v1/billing/invoices
```

The host starts `bin/billing` as a child process on a private unix socket and
mounts it at `/v1/billing`. Requests arrive over ZAP; nothing is re-encoded in
between.

## Publish once, install anywhere

`Path` is for running this example from a checkout. In a deployment the plugin
is built **once**, by CI, and every host installs the same bits:

```yaml
# hanzo.yml — the same file that already declares images: and test:
binaries:
  - name: billing
    main: ./cmd/billing
    platforms: [linux/amd64, linux/arm64]
```

On a tag, `hanzoai/ci` cross-compiles it `CGO_ENABLED=0 -trimpath`, takes the
SHA-256, and attaches both to that release:

```
https://github.com/<owner>/<repo>/releases/download/<tag>/billing-linux-arm64
https://github.com/<owner>/<repo>/releases/download/<tag>/binaries.json   ← {name,os,arch,url,sha256}
```

A host names the artifact and its digest:

```go
zip.Load(zip.Plugin{
    Name: "billing",
    URL:  "https://github.com/hanzoai/billing/releases/download/v1.2.3/billing-linux-arm64",
    Sum:  "9f2c…",           // required with URL
    Dir:  "/var/lib/hanzo",  // not /tmp — that is RAM on most hosts
}, "/v1/billing")
```

`Sum` is not a checksum you may skip. Fetching code over a network and running
it is the one place a host becomes an arbitrary-code-execution vector, so an
unverified download is **refused**, and the digest is checked before the file is
made executable or given its final name — a substituted artifact is a missing
plugin, never a wrong one.

The digest is also the cache key, which is what makes this cheap to operate: a
restart re-runs bits already on disk without touching the network, and rolling
back to a version this host has run before is free and offline. `app.Plugins()`
then reports `Source: "url"` and `Version: <sha256>` — a version identifier that
cannot drift from what is running, because it *is* what is running.

Neither side rebuilds for the other: the plugin's repo ships an artifact, the
host's deployment names one. Pass the URL and Sum in as configuration and even
picking a new plugin build stops being a host rebuild.

## Why a process and not a linked-in object

Go can map a shared object in but never unmap one, so an in-process plugin's
code, globals and goroutines are unreclaimable for the life of the host, and
`-buildmode=plugin` additionally needs cgo and byte-identical builds of every
shared dependency. A child process releases every byte when it exits, works
under `CGO_ENABLED=0`, and needs no build-time agreement with its host.

## What it buys

The host links zip and a transport, never the plugin's dependency graph — so
the host's link time does not grow when the plugin does, plugins build in
parallel, and changing one rebuilds only itself. `go:embed` keeps the
deployment a single artifact.

`app.Reload(name, bin)` swaps a running plugin for a new build without dropping
a request. The replacement must be listening before any traffic moves to it, so
a bad build cannot take the route down, and routes register once and resolve
their target per request, so repeated reloads stay flat in memory.

## The shape

`Service` is `func(*App) error`. A linked-in service and a plugin are the same
type, so `app.Add` composes them identically and where a service runs is a
deployment decision rather than a code change.
