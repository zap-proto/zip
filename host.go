package zip

import (
	"context"
	"fmt"
)

// A program describes an application; a HOST runs one.
//
//	host, err := zip.Serve(app, ":8080")   // build, validate, freeze, serve
//	host.Include(payments)                 // live change: next generation
//	host.Drop(users)
//	host.Reload(billingV2)
//	host.Close()
//
// Include, Drop and Reload are HOST verbs, not App verbs. [App.Use] extends a
// program; Include publishes a new generation of a running one. Different
// worlds, different receivers — and the receiver is the whole of the difference,
// because it decides which questions are askable.
//
// # What host anchoring deleted
//
// When Include lived on *App, a change could land on a definition that several
// hosts had composed, and answering "which hosts does this reach?" needed a
// process-level registry of live servers, reachability filtering over each
// one's walk, a lock order, and an all-or-nothing commitment across them.
// Anchoring the verb to the host makes that question unaskable: host.Include
// affects one host's tree, and a host knows its own generations.
//
// A SHARED DEFINITION IS IMMUTABLE EVERYWHERE. Changing a shared subsystem
// means building a new version and Reloading it at each host that wants it.
// Hosts on different versions mid-rollout is ordinary deployment reality, not a
// hazard this runtime papers over — and the guarantee that machinery replaced
// conceded in its own text that cutover was never simultaneous.
type Host struct {
	app  *App
	errc chan error
}

// Serve builds the program, validates it completely, freezes every definition it
// reaches, and starts serving it on the given addresses.
//
// A program that does not compose never serves and never freezes anything: the
// error carries every conflict, with trails, and nothing is installed.
//
// It returns once the build has succeeded and the listeners have been STARTED —
// not once they are bound. Binding is asynchronous and the transports do not
// report it, so a caller that must know the socket is accepting should dial it
// or read the readiness endpoint. Reporting "bound" from here would be the same
// lie App.listening tells: it is incremented when the servers are constructed,
// which is before any of them has touched a socket.
func Serve(app *App, addrs ...string) (*Host, error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("zip: Serve needs at least one address")
	}
	h, err := host(app)
	if err != nil {
		return nil, err
	}
	// Buffered, so the serving goroutine can finish whether or not anyone ever
	// calls Wait. A nil channel here would make Wait return nil immediately —
	// which is exactly how App.Listen briefly stopped serving at all.
	h.errc = make(chan error, 1)
	go func() { h.errc <- app.listenOn(addrs) }()
	return h, nil
}

// host builds, validates, freezes and installs generation 0 WITHOUT starting a
// listener. Serve is this plus the listeners; a test that needs the host verbs
// without a socket uses it directly, so both go through one build path.
func host(app *App) (*Host, error) {
	app.prepare()
	app.buildMu.Lock()
	g, err := app.build()
	if err == nil {
		app.install(g)
	}
	app.buildMu.Unlock()
	if err != nil {
		return nil, err
	}
	return &Host{app: app}, nil
}

// Wait blocks until the listeners stop, and returns why.
//
// [App.Listen] is exactly Serve followed by this, which is the whole of the
// relationship between them: there is ONE way to start serving a program, and
// Listen is the terminal spelling of it for a main that will never change the
// program at run time.
func (h *Host) Wait() error {
	if h.errc == nil {
		return nil // built but never given listeners
	}
	return <-h.errc
}

// App returns the program this host is running. It is frozen: compose before
// serving, or go through the host's verbs.
func (h *Host) App() *App { return h.app }

// Generation reports which generation is live.
func (h *Host) Generation() uint64 { n, _ := h.app.Generation(); return n }

// Registry reflects the LIVE generation — the ops it publishes, at the paths and
// under the ids its occurrences give them.
func (h *Host) Registry() []*registeredOp { return h.app.Registry() }

// Include publishes a new generation with cs composed in.
//
// Transactional: the next generation is built and validated completely before
// anything swaps, so a plugin whose patterns collide with the live set fails the
// build — with trails — and the previous generation keeps serving.
func (h *Host) Include(cs ...Component) error {
	site := here(1)
	return h.app.transact(site, func() { h.app.compose(site, cs) })
}

// Drop publishes a new generation without the named definitions.
//
// Identity is the POINTER, which is what makes this expressible: an entry is a
// reference, and the definition is the thing being named. It drops every
// reference the host's own program holds — a definition reached through a group
// is referenced by the GROUP, so dropping it means dropping the group.
//
// Routing-level only. Go's plugin package has no Close and never unloads a .so,
// so to reclaim memory run the subsystem out of process behind [Mount].
func (h *Host) Drop(defs ...*App) error {
	site := here(1)
	return h.app.transact(site, func() {
		drop := make(map[*App]bool, len(defs))
		for _, d := range defs {
			drop[d] = true
		}
		kept := h.app.entries[:0:0]
		for _, e := range h.app.entries {
			if child, ok := e.n.(*App); ok && drop[child] {
				continue
			}
			kept = append(kept, e)
		}
		h.app.entries = kept
		version.Add(1)
	})
}

// Reload swaps a subsystem for a new version of itself, in one generation.
//
// A definition is frozen once it has served, so there is no such thing as
// changing one in place — a new version is a NEW definition, and Reload is drop
// the old plus include the new, atomically. Matching is by [Config.AppName],
// which is the one name a definition carries.
func (h *Host) Reload(next ...*App) error {
	site := here(1)
	return h.app.transact(site, func() {
		names := make(map[string]bool, len(next))
		for _, n := range next {
			if n.cfg.AppName == "" {
				panic(fmt.Sprintf("zip: %s: Reload needs a definition with an AppName — "+
					"the name is what says WHICH version this replaces", site))
			}
			names[n.cfg.AppName] = true
		}
		kept := h.app.entries[:0:0]
		for _, e := range h.app.entries {
			if child, ok := e.n.(*App); ok && names[child.cfg.AppName] {
				continue
			}
			kept = append(kept, e)
		}
		h.app.entries = kept
		for _, n := range next {
			h.app.entries = append(h.app.entries, entry{n: n, site: site})
			h.app.OnShutdown(n.ShutdownWithContext)
		}
		version.Add(1)
	})
}

// Close stops every listener and runs teardown hooks.
func (h *Host) Close() error { return h.app.Shutdown() }

// CloseWithContext is Close bounded by ctx.
func (h *Host) CloseWithContext(ctx context.Context) error {
	return h.app.ShutdownWithContext(ctx)
}

// host2 is [host] under a name that does not collide with a local variable
// called host, which every test that builds one wants to use.
func host2(app *App) (*Host, error) { return host(app) }
