package zip

import (
	"cmp"
	"slices"
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

// Evict reclaims plugin processes under two bounds and reports how many it
// stopped: first every lazy plugin that has not served for its IdleAfter, then,
// while more than warm are still running, the least recently used. Zero warm
// means only the first bound applies.
//
// One pass, no goroutine — the caller decides the cadence, so a host under test
// can drive it directly and a host in production can put it on the ticker it
// already has.
//
// THE COUNT IS NOT THIS FUNCTION'S TO ENFORCE ALONE, and an earlier version of
// this comment claimed otherwise: warm was an ARGUMENT here, on the reasoning
// that the number is a fact about the host and not the library. The reasoning
// held; the placement did not. A bound consulted once a minute is outrun by
// anything that starts faster, and a fan-out door that asks every subsystem at
// once does — a hundred children inside ninety seconds, so the process held them
// all until the next sweep, stopped answering its own liveness probe, and was
// killed with the ceiling never applied. Warm is state now and makeRoom applies
// it at the start path; this still runs it, because age and count are one sweep.
//
// Draining uses the plugin's own Drain, so a request in flight when the sweep
// lands finishes on the old process exactly as it would across a Reload.
func (a *App) Evict() int {
	now := time.Now()
	stopped := 0
	for _, p := range a.pluginSet() {
		if p.evictIfIdle(now) {
			stopped++
		}
	}
	return stopped + a.evictOver(a.warm, now)
}

// host is the ROOT app of the composition this plugin belongs to, which is the
// only app that can answer how many processes are running and what the budget is.
// Falls back to the defining app for a plugin whose composition was never built —
// a Load'ed service used directly in a test, where the two are the same thing.
func (p *plugin) host() *App {
	if o := p.owner.Load(); o != nil {
		return o
	}
	return p.app
}

// makeRoom is the ceiling, and it runs where a process is about to exist rather
// than on a ticker. Called from the start path with the starter's own lock held,
// so it must never consider the starter itself: evicting p here would deadlock on
// p.mu, and p is the one plugin about to be needed anyway.
//
// This is the half a sweep cannot do. Evict trims to warm once a minute, which is
// a bound only while starts arrive slower than that. Measured on a host whose MCP
// door asks every subsystem at once: ~100 children started inside 90 seconds, the
// pod stopped answering its own liveness probe, and the kubelet killed it — the
// "ceiling" observed only in the logs of a container that was already gone.
func (a *App) makeRoom(starter *plugin) {
	if a.warm <= 0 {
		return
	}
	now := time.Now()
	for {
		var (
			live      int
			evictable []*plugin
		)
		for _, p := range a.pluginSet() {
			if p.cur.Load() == nil {
				continue
			}
			live++
			if p != starter && p.spec.Lazy && p.spec.idleAfter() > 0 {
				evictable = append(evictable, p)
			}
		}
		// The starter is not running yet, so room for it means strictly under.
		if live < a.warm || len(evictable) == 0 {
			return
		}
		slices.SortFunc(evictable, func(x, y *plugin) int {
			return cmp.Compare(x.lastUse.Load(), y.lastUse.Load())
		})
		cold := evictable[0]
		if !cold.evict("room", now.Sub(time.Unix(0, cold.lastUse.Load()))) {
			return // already down; re-reading would spin
		}
	}
}

// pluginSet is every plugin this host and its composed hosts hold, read once
// under each host's lock. Both passes need the same set and neither may hold a
// lock while stopping a child, because retire() takes the plugin's own.
func (a *App) pluginSet() []*plugin {
	var all []*plugin
	for _, h := range a.hosts() {
		h.plugMu.Lock()
		for _, p := range h.plugins {
			all = append(all, p)
		}
		h.plugMu.Unlock()
	}
	return all
}

// evictOver stops the least recently used evictable plugins until at most warm
// remain running. Zero means unbounded, which is the historical behaviour.
//
// AGE BOUNDS THE STEADY STATE AND COUNT BOUNDS THE BURST, which is why both
// exist. IdleAfter can only reclaim a plugin that has already gone quiet for
// its whole window, so during the minutes after a cold start — when every
// prefix that gets a request starts a child and none is old enough to be idle —
// it reclaims nothing at all. Measured on a host of this shape: 37 children at
// ~152MiB each in steady state, and an OOM kill six minutes after boot, well
// before the first plugin was 15 minutes idle. A count is a bound the burst
// cannot outrun.
//
// LEAST RECENT USE, because the plugin that has gone longest without a request
// is the one whose restart is least likely to be paid for by a caller waiting.
//
// The cap counts every RUNNING plugin, including the ones it may not stop: they
// hold the same memory, so counting only the evictable ones would authorise the
// budget twice. When the unevictable alone exceed warm this reclaims what it
// can and leaves the rest — a host is better over its budget than deprived of
// the identity or config service every other call goes through.
func (a *App) evictOver(warm int, now time.Time) int {
	if warm <= 0 {
		return 0
	}
	var (
		live      int
		evictable []*plugin
	)
	for _, p := range a.pluginSet() {
		if p.cur.Load() == nil {
			continue
		}
		live++
		if p.spec.Lazy && p.spec.idleAfter() > 0 {
			evictable = append(evictable, p)
		}
	}
	if live <= warm {
		return 0
	}
	// Ascending by last use, so the front of the slice is the coldest. A
	// plugin's lastUse is stamped on every resolve, and only stamped for the
	// ones collected above, so the ordering is total.
	slices.SortFunc(evictable, func(x, y *plugin) int {
		return cmp.Compare(x.lastUse.Load(), y.lastUse.Load())
	})
	stopped := 0
	for _, p := range evictable {
		if live-stopped <= warm {
			break
		}
		if p.evict("lru", now.Sub(time.Unix(0, p.lastUse.Load()))) {
			stopped++
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
	// No lock: the swap that takes the child down happens under p.mu inside
	// evict, and this read only decides whether to ask. A request landing
	// between the two is safe either way — it is already being served by the
	// instance retire() is about to DRAIN, or it arrives after the swap and
	// starts a fresh child. So the check buys freshness, not correctness, and
	// holding the lock across it would contend with the request path to learn
	// something it cannot guarantee anyway.
	last := p.lastUse.Load()
	if last == 0 || now.Sub(time.Unix(0, last)) < after {
		return false
	}
	return p.evict("idle", now.Sub(time.Unix(0, last)))
}

// evict stops p's current instance and reports whether it stopped one. It is
// the one place a plugin is reclaimed, so the two policies above cannot come to
// disagree about how a child is taken down; reason says which asked.
func (p *plugin) evict(reason string, idle time.Duration) bool {
	p.mu.Lock()
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
			"name", p.name, "pid", in.cmd.Process.Pid, "reason", reason,
			"idle", idle.Round(time.Second).String())
	}
	p.retire(in, p.spec.Drain)
	return true
}

// Reap runs Evict on a ticker until the returned function is called. A host
// that wants the behaviour writes one line; a host that does not is unaffected,
// because nothing here runs unless it is asked for.
func (a *App) Reap(every time.Duration) (stop func()) {
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
				a.Evict()
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
