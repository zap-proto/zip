package zip

import (
	"cmp"
	"fmt"
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
//
// A BUSY PROCESS IS NEVER THE ONE STOPPED. Ranking by when a request started, and
// not counting the ones in flight, stopped a subsystem two to eleven seconds into
// a request it was still answering: the caller read "http: read response: EOF" as
// a 502, and the host evicted 162 times in eight minutes, each stop the next
// start's cold miss. Candidates are idle instances, coldest by when their last
// request ended. When every candidate is busy the starter waits for one to finish,
// as long as its own start may take, and then gives up with an error — the caller
// answers 503 and nothing in flight is killed.
//
// On success the starter holds one unit of room until it calls started.
func (a *App) makeRoom(starter *plugin, deadline time.Time) error {
	if a.warm <= 0 {
		return nil
	}
	for {
		room, live, busy := a.room(starter)
		if room {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d of %d processes running, %d busy and none idle", live, a.warm, busy)
		}
		time.Sleep(roomPoll)
	}
}

// room is one look for room under roomMu, and reports whether it took one.
//
// Over the ceiling is admitted only when nothing could ever make room: no
// process this host may stop, busy or idle, and no start in flight, which
// becomes such a process once it runs. Admitting while starts are in flight let
// a cold burst of eight first requests start eight processes against a ceiling
// of two.
func (a *App) room(starter *plugin) (ok bool, live, busy int) {
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	live, idle, busy := a.census(starter)
	if live+a.starting < a.warm || len(idle) == 0 && busy == 0 && a.starting == 0 {
		a.starting++
		return true, live, busy
	}
	// Coldest first, and on to the next when one cannot be stopped: a plugin
	// mid-Reload holds its lock, and the room behind it is still room.
	for _, p := range idle {
		seen := p.lastUse.Load()
		if p.evict("room", time.Since(time.Unix(0, seen)), seen) {
			a.starting++
			return true, live, busy
		}
	}
	return false, live, busy
}

// roomPoll is how often a starter waiting for room looks again. A request on the
// process it is waiting for takes far longer than this to finish.
const roomPoll = 10 * time.Millisecond

// started returns the unit of room makeRoom handed out: the process is now
// counted as running, or it failed to start and holds nothing.
func (a *App) started() {
	if a.warm <= 0 {
		return
	}
	a.roomMu.Lock()
	a.starting--
	a.roomMu.Unlock()
}

// census counts the running processes, and among the ones this host may stop
// returns the idle ones coldest first and how many are busy. The starter is
// never a candidate.
func (a *App) census(starter *plugin) (live int, idle []*plugin, busy int) {
	for _, p := range a.pluginSet() {
		in := p.cur.Load()
		if in == nil {
			continue
		}
		live++
		if p == starter || !p.evictable() {
			continue
		}
		if in.busy.Load() > 0 {
			busy++
			continue
		}
		idle = append(idle, p)
	}
	slices.SortFunc(idle, func(x, y *plugin) int {
		return cmp.Compare(x.lastUse.Load(), y.lastUse.Load())
	})
	return live, idle, busy
}

// evictable is whether the ceiling and the age bound may stop p at all: only a
// lazy plugin with an idle bound, because the next request has to bring it back.
func (p *plugin) evictable() bool { return p.lazy && p.idle > 0 }

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
		in := p.cur.Load()
		if in == nil {
			continue
		}
		live++
		if p.evictable() && in.busy.Load() == 0 {
			evictable = append(evictable, p)
		}
	}
	if live <= warm {
		return 0
	}
	// Ascending by last use, so the front of the slice is the coldest. lastUse
	// is stamped when a request ENDS, so a plugin that has been answering for a
	// minute is not mistaken for one asked a minute ago and since forgotten.
	slices.SortFunc(evictable, func(x, y *plugin) int {
		return cmp.Compare(x.lastUse.Load(), y.lastUse.Load())
	})
	stopped := 0
	for _, p := range evictable {
		if live-stopped <= warm {
			break
		}
		lruSeen := p.lastUse.Load()
		if p.evict("lru", now.Sub(time.Unix(0, lruSeen)), lruSeen) {
			stopped++
		}
	}
	return stopped
}

// evictIfIdle stops p's current instance if it is lazy, running, and has been
// unused for at least its IdleAfter. It reports whether it stopped one.
func (p *plugin) evictIfIdle(now time.Time) bool {
	after := p.idle
	if after <= 0 || !p.lazy {
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
	return p.evict("idle", now.Sub(time.Unix(0, last)), last)
}

// evict stops p's current instance and reports whether it stopped one. It is
// the one place a plugin is reclaimed, so the two policies above cannot come to
// disagree about how a child is taken down; reason says which asked.
func (p *plugin) evict(reason string, idle time.Duration, seen int64) bool {
	// A plugin whose lock is held is mid-transition — starting, reloading,
	// unloading — and is nobody's candidate. Waiting for it would hold the room
	// lock across a child's whole start.
	if !p.mu.TryLock() {
		return false
	}
	if p.closed || p.disabled.Load() {
		p.mu.Unlock()
		return false
	}
	// THE CLOCK IS RE-READ UNDER THE LOCK.
	//
	// The caller decided to evict from a lastUse it read outside the lock. A
	// request arriving in that window does not just get served by the instance
	// about to drain — it can START one, because target() brings a lazy plugin
	// up with a CAS on p.cur. The swap below then takes that brand-new child
	// down, and the caller which just started it waits out the drain and gets a
	// 502. In production this read as "zip lazy plugin started on first request"
	// and "zip idle plugin evicted · idle 2h37m58s" at the SAME timestamp, over
	// and over: every Slack event 502'd after 43 to 64 seconds and no agent run
	// ever began.
	//
	// lastUse moving is exactly the signal that someone used or started it since
	// the decision, so the decision is stale and the eviction is refused. seen
	// of 0 means the caller is not making an idleness claim.
	if seen != 0 && p.lastUse.Load() != seen {
		p.mu.Unlock()
		return false
	}
	in := p.cur.Load()
	if in == nil {
		p.mu.Unlock()
		return false // already down; nothing to stop
	}
	// NOTHING IN FLIGHT, decided against every request that will ever reach in.
	// closing is raised before busy is read, and claim raises busy before it
	// reads closing, so a request either sees closing and goes to the lock this
	// holds, or is counted here and keeps the instance.
	in.closing.Store(true)
	if in.busy.Load() > 0 {
		in.closing.Store(false)
		p.mu.Unlock()
		return false
	}
	// Swap to nil BEFORE the child dies so supervise()'s CAS fails and it does
	// not treat this as a crash to recover from. Not disabled: the next request
	// through target() is meant to bring it back.
	if !p.cur.CompareAndSwap(in, nil) {
		p.mu.Unlock()
		return false // it exited on its own; the supervisor has it
	}
	drain := p.spec.Drain // read under the lock Reload writes spec under
	p.mu.Unlock()

	p.evictions.Add(1)
	if p.app != nil {
		p.app.logger.Info("zip idle plugin evicted",
			"name", p.name, "pid", in.cmd.Process.Pid, "reason", reason,
			"idle", idle.Round(time.Second).String())
	}
	p.retire(in, drain)
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
