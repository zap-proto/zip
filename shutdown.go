package zip

import (
	"context"
	"errors"
)

// OnShutdown registers fn as a teardown hook, run during Shutdown /
// ShutdownWithContext. This is the one teardown primitive zip exposes:
// subsystems register their own cleanup at mount time, and reverse-mount
// teardown falls out for free (see the ordering note below).
//
// Ordering. Hooks run LAST in the shutdown sequence — after listeners stop
// accepting and after in-flight requests drain — and in LIFO order (reverse
// registration = reverse mount order). Draining first means a subsystem's
// teardown never races the requests still using it; LIFO means a dependency
// mounted before its dependents is torn down after them.
//
// Errors. Every hook runs even if an earlier one fails; all hook errors (and
// the drain error) are aggregated with errors.Join and returned from Shutdown.
//
// Concurrency. Registration is safe from multiple goroutines. A nil fn is
// ignored. Registering after Shutdown has begun is a no-op: the hook is
// dropped (never run) and a warning is logged — there is no longer a shutdown
// to hook into, and running it immediately would give OnShutdown two meanings
// depending on timing. Register teardown at mount time, before Shutdown.
func (a *App) OnShutdown(fn func(context.Context) error) {
	if fn == nil {
		return
	}
	a.hookMu.Lock()
	if a.shuttingDown {
		a.hookMu.Unlock()
		a.logger.Warn("zip: OnShutdown called after Shutdown; hook dropped")
		return
	}
	a.hooks = append(a.hooks, fn)
	a.hookMu.Unlock()
}

// shutdown is the single graceful-shutdown sequence behind Shutdown and
// ShutdownWithContext. It is idempotent: the first call claims shutdown under
// hookMu (snapshotting and clearing the hooks) and every later call returns
// nil without repeating any step, so hooks run at most once.
//
// Sequence: (1) stop every transport accepting new connections, and wait for
// the connections an HTTP transport holds, bounded by ctx; (2) drain in-flight
// requests via fiber, bounded by ctx; (3) run teardown hooks LIFO, passing ctx
// to each; (4) wait for every plugin child to exit. All errors are joined and
// returned.
func (a *App) shutdown(ctx context.Context) error {
	a.hookMu.Lock()
	if a.shuttingDown {
		a.hookMu.Unlock()
		return nil
	}
	a.shuttingDown = true
	hooks := a.hooks
	a.hooks = nil
	a.hookMu.Unlock()

	// 1. Stop accepting new connections on every transport listener, and drain
	//    the connections HTTP holds until ctx ends.
	a.closeServers(ctx)
	a.closeMCP()
	// 2. Drain in-flight requests (graceful; bounded by ctx).
	// A program that was never served has no router to drain.
	var errs []error
	if g := a.live.Load(); g != nil {
		errs = append(errs, g.router.ShutdownWithContext(ctx))
	}
	// 3. Tear subsystems down in reverse mount order (LIFO). Run ALL hooks
	//    even if some fail; aggregate every error.
	nested := ctx.Value(nestedShutdown{}) != nil
	hookCtx := context.WithValue(ctx, nestedShutdown{}, true)
	for i := len(hooks) - 1; i >= 0; i-- {
		if err := hooks[i](hookCtx); err != nil {
			errs = append(errs, err)
		}
	}
	// 4. Wait for every plugin child to exit. The hooks only signalled them, so
	//    they drain together, and each is killed at its own grace. That grace,
	//    not ctx, bounds this wait: ctx has usually been spent on the drain, and
	//    a host that returned first would be torn down under its children (a
	//    container stops every process when its first one exits). A composed app's
	//    shutdown runs as its parent's hook and leaves the wait to the
	//    outermost one, which walks the whole composition: waiting at each level
	//    would drain the children one after another.
	if !nested {
		if g, err := a.liveOrBuild(); err == nil {
			awaitChildren(g.hosts)
		}
	}
	return errors.Join(errs...)
}

// nestedShutdown marks the context a parent hands its teardown hooks, so a
// composed app's shutdown knows it is not the outermost.
type nestedShutdown struct{}

// awaitChildren blocks until no plugin in hosts has a process left to stop.
func awaitChildren(hosts []*App) {
	for _, h := range hosts {
		h.plugMu.Lock()
		ps := make([]*plugin, 0, len(h.plugins))
		for _, p := range h.plugins {
			ps = append(ps, p)
		}
		h.plugMu.Unlock()
		for _, p := range ps {
			p.stopping.Wait()
		}
	}
}
