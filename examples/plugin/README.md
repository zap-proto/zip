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
