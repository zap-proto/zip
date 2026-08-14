package zip

import (
	"sync"
	"time"
)

// Idle plugins cost what a running plugin costs, which is not nothing.
//
// A host that composes many services pays for every one it has ever served,
// forever: Lazy defers the first start, and nothing has ever stopped one. On a
// host running two dozen subsystems that is the dominant resident cost — each
// child holds its own Go heap, and the total tracks the SIZE OF THE CATALOG
// rather than the traffic. Measured on one such host: 24 children, ~150MiB of
// live heap each, ~4.4GiB resident, while three of them (the ones that do
// little) sat at 12-20MiB. The catalog was the bill.
//
// Eviction is the other half of Lazy, and it is deliberately not Unload:
// Unload means "stay down", eviction means "you may go, come back when asked".
// The next request through target() starts it again by the path that already
// exists, so the surface never changes — only the process is transient.
//
// WHY THIS IS SAFE AGAINST THE SUPERVISOR. supervise() restarts a child whose
// process exits, and distinguishes a deliberate stop by CAS-ing cur from the
// instance it supervises to nil: if that fails, someone else already claimed
// the retirement and it returns. Eviction clears cur BEFORE the child dies, so
// that CAS always fails and no restart storm follows. It is the same contract
// Unload and Reload use, minus the disabled flag.

// IdleAfter is how long a lazy plugin may go unused before its process is
// stopped. Zero means never — the historical behaviour, and the right setting
// for a plugin whose start is expensive or whose first request must not pay a
// cold start (an identity or config service every other call goes through).
//
// It applies only to Lazy plugins: an eager one was started deliberately at
// Load and stopping it would contradict that.
func (p Plugin) idleAfter() time.Duration { return p.IdleAfter }

// EvictIdle stops every lazy plugin that has not served a request for its
// IdleAfter, and reports how many it stopped. One pass, no goroutine — the
// caller decides the cadence, so a host under test can drive it directly and a
// host in production can put it on the ticker it already has.
//
// Draining uses the plugin's own Drain, so a request in flight when the sweep
// lands finishes on the old process exactly as it would across a Reload.
func (a *App) EvictIdle() int {
	now := time.Now()
	stopped := 0
	for _, h := range a.hosts() {
		h.plugMu.Lock()
		named := make([]*plugin, 0, len(h.plugins))
		for _, p := range h.plugins {
			named = append(named, p)
		}
		h.plugMu.Unlock()

		for _, p := range named {
			if p.evictIfIdle(now) {
				stopped++
			}
		}
	}
	return stopped
}

// evictIfIdle stops p's current instance if it is lazy, running, and has been
// unused for at least its IdleAfter. It reports whether it stopped one.
func (p *plugin) evictIfIdle(now time.Time) bool {
	after := p.spec.idleAfter()
	if after <= 0 || !p.spec.Lazy {
		return false
	}
	// Read before taking the lock: an idle plugin is the common case and there
	// is no reason to contend with the request path to decide it is not.
	last := p.lastUse.Load()
	if last == 0 || now.Sub(time.Unix(0, last)) < after {
		return false
	}

	p.mu.Lock()
	// Re-checked under the lock. A request between the read above and here
	// starts a new instance or refreshes lastUse, and evicting then would stop
	// a plugin that is serving.
	if last = p.lastUse.Load(); last == 0 || now.Sub(time.Unix(0, last)) < after {
		p.mu.Unlock()
		return false
	}
	if p.closed || p.disabled.Load() {
		p.mu.Unlock()
		return false
	}
	// Swap to nil BEFORE the child dies so supervise()'s CAS fails and it does
	// not treat this as a crash to recover from. Not disabled: the next request
	// through target() is meant to bring it back.
	in := p.cur.Swap(nil)
	p.mu.Unlock()

	if in == nil {
		return false // already down; nothing to stop
	}
	p.evictions.Add(1)
	if p.app != nil {
		p.app.logger.Info("zip idle plugin evicted",
			"name", p.name, "pid", in.cmd.Process.Pid,
			"idle", now.Sub(time.Unix(0, last)).Round(time.Second).String())
	}
	p.retire(in, p.spec.Drain)
	return true
}

// ReapIdle runs EvictIdle on a ticker until the returned function is called.
// A host that wants the behaviour writes one line; a host that does not is
// unaffected, because nothing here runs unless it is asked for.
func (a *App) ReapIdle(every time.Duration) (stop func()) {
	if every <= 0 {
		every = time.Minute
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				a.EvictIdle()
			}
		}
	}()
	// stop is SYNCHRONOUS: it returns only once a sweep in progress has
	// finished. Signalling and returning is not enough — a sweep reads App
	// state that Shutdown writes, so a reaper still running when the host began
	// shutting down is a data race, and it is the one the race detector found.
	//
	// sync.Once, not a bool: a host that stops the reaper from two places (a
	// defer and an explicit shutdown path) would otherwise race on the flag and
	// double-close the channel. The second call still waits, which is correct —
	// finished is closed by then, so it returns at once.
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}
