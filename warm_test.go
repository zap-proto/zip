package zip_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// warmHost loads n lazy plugins that never age out, so anything these tests see
// evicted was evicted by the COUNT bound and not by IdleAfter. Every one is
// warmed by a request, because a plugin nothing asked for is not running and
// would not count toward the bound in the first place.
//
// Dir is sockDir(t) and not t.TempDir(): a unix socket path is bounded at ~108
// bytes, and t.TempDir() spells the TEST'S NAME into it, so a descriptive name
// pushes the child's listen past the limit and it dies with `bind: invalid
// argument` — which surfaces as a 503 that reads exactly like a lazy plugin
// declining to start.
func warmHost(t *testing.T, n int, idle time.Duration) *zip.App {
	t.Helper()
	bin := buildPlugin(t, "v1")
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	for i := range n {
		name := fmt.Sprintf("demo%d", i)
		app.Use(must(zip.Load(
			zip.Plugin{
				Name: name, Bin: bin, Dir: sockDir(t), Lazy: true,
				IdleAfter: idle,
			}, "/v1/"+name,
		)))
	}
	t.Cleanup(func() { _ = app.Shutdown() })

	// Warmed oldest-first with a gap, so last-use order is demo0 < demo1 < ...
	// and the coldest is deterministic rather than whatever the clock did.
	for i := range n {
		if status, _ := call(t, app, "GET", fmt.Sprintf("/v1/demo%d/version", i), ""); status != 200 {
			t.Fatalf("demo%d did not serve", i)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := len(runningPIDs(t, app)); got != n {
		t.Fatalf("warmed %d plugins, want %d", got, n)
	}
	return app
}

// TestWarm_StopsTheColdestUntilUnderTheBound is the bound doing its job, and it
// checks WHICH were stopped rather than only how many: a policy that evicted the
// hottest would satisfy a count assertion and be exactly backwards.
func TestWarm_StopsTheColdestUntilUnderTheBound(t *testing.T) {
	const plugins, warm = 6, 2
	app := warmHost(t, plugins, time.Hour) // an hour: nothing can age out here

	if n := app.Evict(warm); n != plugins-warm {
		t.Fatalf("Evict(%d) stopped %d of %d, want %d", warm, n, plugins, plugins-warm)
	}
	if !settles(3*time.Second, func() bool { return len(runningPIDs(t, app)) == warm }) {
		t.Fatalf("%d still running, want %d", len(runningPIDs(t, app)), warm)
	}

	// The survivors must be the two most recently used, demo4 and demo5.
	live := map[string]bool{}
	for _, p := range app.Plugins() {
		if p.Running {
			live[p.Name] = true
		}
	}
	for _, name := range []string{"demo4", "demo5"} {
		if !live[name] {
			t.Errorf("%s was evicted, but it was among the %d most recently used", name, warm)
		}
	}
	for _, name := range []string{"demo0", "demo1", "demo2", "demo3"} {
		if live[name] {
			t.Errorf("%s survived, but it was colder than the survivors", name)
		}
	}
}

// TestWarm_ZeroIsUnbounded pins the default. A host that never passes a bound
// must keep the behaviour it had before the bound existed.
func TestWarm_ZeroIsUnbounded(t *testing.T) {
	const plugins = 4
	app := warmHost(t, plugins, time.Hour)

	if n := app.Evict(0); n != 0 {
		t.Fatalf("Evict(0) stopped %d — zero must mean unbounded", n)
	}
	if got := len(runningPIDs(t, app)); got != plugins {
		t.Fatalf("%d running after Evict(0), want %d", got, plugins)
	}
}

// TestWarm_UnderTheBoundStopsNothing: the bound is a ceiling, not a target. A
// host holding fewer processes than it may must not pay a cold start to reach it.
func TestWarm_UnderTheBoundStopsNothing(t *testing.T) {
	app := warmHost(t, 2, time.Hour)

	if n := app.Evict(5); n != 0 {
		t.Fatalf("Evict(5) stopped %d with only 2 running", n)
	}
	if got := len(runningPIDs(t, app)); got != 2 {
		t.Fatalf("%d running, want 2", got)
	}
}

// TestWarm_ExemptPluginsCountButAreNotStopped is the honest half of the bound.
// IdleAfter zero means never-evict, and such a plugin holds memory like any
// other — so it must count against the budget (or the budget is authorised
// twice) and must still not be stopped (or "never" meant nothing).
func TestWarm_ExemptPluginsCountButAreNotStopped(t *testing.T) {
	bin := buildPlugin(t, "v1")
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	// Two exempt (IdleAfter unset) and two evictable.
	for _, p := range []zip.Plugin{
		{Name: "keep0", Bin: bin, Dir: sockDir(t), Lazy: true},
		{Name: "keep1", Bin: bin, Dir: sockDir(t), Lazy: true},
		{Name: "drop0", Bin: bin, Dir: sockDir(t), Lazy: true, IdleAfter: time.Hour},
		{Name: "drop1", Bin: bin, Dir: sockDir(t), Lazy: true, IdleAfter: time.Hour},
	} {
		app.Use(must(zip.Load(p, "/v1/"+p.Name)))
	}
	defer func() { _ = app.Shutdown() }()
	for _, n := range []string{"keep0", "keep1", "drop0", "drop1"} {
		if status, body := call(t, app, "GET", "/v1/"+n+"/version", ""); status != 200 {
			t.Fatalf("%s did not serve: status=%d body=%q", n, status, body)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Four running, bound of two: the only two it MAY stop are the evictable
	// ones, so it stops both and stays over the bound rather than touching the
	// exempt pair.
	if n := app.Evict(2); n != 2 {
		t.Fatalf("Evict(2) stopped %d, want 2 (both evictable)", n)
	}
	if !settles(3*time.Second, func() bool { return len(runningPIDs(t, app)) == 2 }) {
		t.Fatalf("running=%d, want the 2 exempt", len(runningPIDs(t, app)))
	}
	for _, p := range app.Plugins() {
		switch p.Name {
		case "keep0", "keep1":
			if !p.Running {
				t.Errorf("%s was evicted, but IdleAfter unset means never", p.Name)
			}
		case "drop0", "drop1":
			if p.Running {
				t.Errorf("%s survived a bound of 2 with 4 running", p.Name)
			}
		}
	}

	// And a bound BELOW the exempt count reclaims nothing further, rather than
	// looping or stopping something it may not.
	if n := app.Evict(1); n != 0 {
		t.Fatalf("Evict(1) stopped %d when only exempt plugins remain", n)
	}
}

// TestWarm_EvictedRoutesStillAnswer: the bound must not take a route down. This
// is what separates it from Unload, and it is the property a caller notices.
func TestWarm_EvictedRoutesStillAnswer(t *testing.T) {
	app := warmHost(t, 4, time.Hour)
	before := map[string]int{}
	for _, p := range app.Plugins() {
		before[p.Name] = p.PID
	}
	if n := app.Evict(1); n != 3 {
		t.Fatalf("Evict(1) stopped %d of 4, want 3", n)
	}
	if !settles(3*time.Second, func() bool { return len(runningPIDs(t, app)) == 1 }) {
		t.Fatal("did not settle at the bound")
	}

	// demo0 was the coldest, so it was certainly evicted. Asking again must
	// serve, from a DIFFERENT process.
	status, body := call(t, app, "GET", "/v1/demo0/version", "")
	if status != 200 {
		t.Fatalf("demo0 after eviction: status=%d body=%q", status, body)
	}
	for _, p := range app.Plugins() {
		if p.Name == "demo0" {
			if !p.Running || p.PID == 0 {
				t.Fatalf("demo0 did not restart on demand: %+v", p)
			}
			if p.PID == before["demo0"] {
				t.Fatalf("same pid %d — nothing was actually stopped", p.PID)
			}
		}
	}
}

// TestWarm_BothBoundsInOnePass: age and count are one sweep, and the count is
// applied to what age LEFT. Without that ordering the returned figure and the
// live set disagree.
func TestWarm_BothBoundsInOnePass(t *testing.T) {
	const plugins, warm = 5, 3
	app := warmHost(t, plugins, 40*time.Millisecond)

	time.Sleep(80 * time.Millisecond) // every one is now past IdleAfter
	if n := app.Evict(warm); n != plugins {
		t.Fatalf("Evict(%d) stopped %d, want all %d — age runs first and takes them all",
			warm, n, plugins)
	}
	if !settles(3*time.Second, func() bool { return len(runningPIDs(t, app)) == 0 }) {
		t.Fatalf("%d still running", len(runningPIDs(t, app)))
	}
}
