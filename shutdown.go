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
// Sequence: (1) stop every transport accepting new connections; (2) drain
// in-flight requests via fiber, bounded by ctx; (3) run teardown hooks LIFO,
// passing ctx to each. All errors are joined and returned.
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

	// 1. Stop accepting new connections on every transport listener.
	a.closeServers()
	// 2. Drain in-flight requests (graceful; bounded by ctx).
	errs := []error{a.fiber.ShutdownWithContext(ctx)}
	// 3. Tear subsystems down in reverse mount order (LIFO). Run ALL hooks
	//    even if some fail; aggregate every error.
	for i := len(hooks) - 1; i >= 0; i-- {
		if err := hooks[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
