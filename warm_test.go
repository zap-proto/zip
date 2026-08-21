package zip_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// The ceiling is enforced where a plugin STARTS, so most of these tests never
// call Evict: they ask for plugins and read how many processes exist. That is the
// design — a bound restored by a sweep is not a bound, because the burst that
// matters is over before the sweep runs.
//
// Dir is sockDir(t) and not t.TempDir(): a unix socket path is bounded at ~108
// bytes and t.TempDir() spells the TEST'S NAME into it, so a descriptive name
// pushes the child's listen past the limit and it dies with `bind: invalid
// argument` — which surfaces as a 503 that reads exactly like a lazy plugin
// declining to start.
func warmHost(t *testing.T, n, warm int, idle time.Duration) *zip.App {
	t.Helper()
	bin := buildPlugin(t, "v1")
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true, Warm: warm})
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
	return app
}

// ask serves one request per plugin, oldest first with a gap, so last-use order is
// demo0 < demo1 < ... and "the coldest" is deterministic rather than whatever the
// clock did. It returns the PEAK number of processes seen along the way, which is
// the figure a trim-afterwards implementation gets wrong.
func ask(t *testing.T, app *zip.App, n int) (peak int) {
	t.Helper()
	for i := range n {
		if status, body := call(t, app, "GET", fmt.Sprintf("/v1/demo%d/version", i), ""); status != 200 {
			t.Fatalf("demo%d did not serve: status=%d body=%q", i, status, body)
		}
		if live := len(runningPIDs(t, app)); live > peak {
			peak = live
		}
		time.Sleep(2 * time.Millisecond)
	}
	return peak
}

// TestWarm_CeilingHoldsDuringABurst is the property a sweep cannot provide, and
// the one whose absence took a production pod down. NO sweep runs here — no Reap,
// no Evict — so the only thing that can hold the line is the start path. The count
// is read after EVERY request, because the end is exactly where a
// trim-afterwards implementation also looks correct.
func TestWarm_CeilingHoldsDuringABurst(t *testing.T) {
	const plugins, warm = 12, 4
	app := warmHost(t, plugins, warm, time.Hour) // an hour: age can reclaim nothing

	peak := ask(t, app, plugins)
	if peak > warm {
		t.Fatalf("peak %d processes against a ceiling of %d — the bound is being "+
			"restored by a sweep instead of enforced at the start", peak, warm)
	}
	if peak != warm { // or this proves only that twelve fit
		t.Errorf("peak was %d, want exactly the ceiling %d", peak, warm)
	}
	t.Logf("%d sequential cold starts, peak live %d, ceiling %d", plugins, peak, warm)

	// A ceiling is not an Unload: the route evicted first still answers.
	if status, _ := call(t, app, "GET", "/v1/demo0/version", ""); status != 200 {
		t.Fatal("the coldest plugin stopped answering after being evicted for room")
	}
}

// TestWarm_KeepsTheMostRecentlyUsed checks WHICH survive, not just how many: a
// policy that shed the hottest would satisfy a count assertion and be exactly
// backwards.
func TestWarm_KeepsTheMostRecentlyUsed(t *testing.T) {
	const plugins, warm = 6, 2
	app := warmHost(t, plugins, warm, time.Hour)
	ask(t, app, plugins)

	live := map[string]bool{}
	for _, p := range app.Plugins() {
		if p.Running {
			live[p.Name] = true
		}
	}
	for _, n := range []string{"demo4", "demo5"} {
		if !live[n] {
			t.Errorf("%s is not running, but it is among the %d most recently used", n, warm)
		}
	}
	for _, n := range []string{"demo0", "demo1", "demo2", "demo3"} {
		if live[n] {
			t.Errorf("%s survived, but it is colder than the survivors", n)
		}
	}
}

// TestWarm_ZeroIsUnbounded pins the default: a host that states no ceiling keeps
// the behaviour it had before one existed.
func TestWarm_ZeroIsUnbounded(t *testing.T) {
	const plugins = 4
	app := warmHost(t, plugins, 0, time.Hour)

	if peak := ask(t, app, plugins); peak != plugins {
		t.Fatalf("peak %d of %d with no ceiling — zero must mean unbounded", peak, plugins)
	}
	if n := app.Evict(); n != 0 {
		t.Fatalf("Evict stopped %d with no ceiling and nothing idle", n)
	}
}

// TestWarm_UnderTheCeilingStopsNothing: it is a ceiling, not a target. A host
// holding fewer processes than it may must not pay a cold start to reach it.
func TestWarm_UnderTheCeilingStopsNothing(t *testing.T) {
	app := warmHost(t, 2, 5, time.Hour)
	if peak := ask(t, app, 2); peak != 2 {
		t.Fatalf("peak %d, want 2", peak)
	}
	if n := app.Evict(); n != 0 {
		t.Fatalf("Evict stopped %d with 2 running under a ceiling of 5", n)
	}
}

// TestWarm_ExemptPluginsCountButAreNotStopped is the honest half. IdleAfter zero
// means never-evict, and such a plugin holds memory like any other — so it must
// COUNT against the ceiling (or the budget is authorised twice) and must still not
// be stopped (or "never" meant nothing). When the exempt alone fill the ceiling the
// host goes OVER it rather than shedding the identity service every other call
// goes through.
func TestWarm_ExemptPluginsCountButAreNotStopped(t *testing.T) {
	bin := buildPlugin(t, "v1")
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true, Warm: 2})
	for _, p := range []zip.Plugin{
		{Name: "keep0", Bin: bin, Dir: sockDir(t), Lazy: true}, // IdleAfter unset: exempt
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

	for _, p := range app.Plugins() {
		switch p.Name {
		case "keep0", "keep1":
			if !p.Running {
				t.Errorf("%s was evicted, but IdleAfter unset means never", p.Name)
			}
		case "drop0":
			if p.Running {
				t.Error("drop0 survived: drop1's start should have taken the coldest evictable")
			}
		}
	}
	// Two exempt plus the one evictable that just served: over the ceiling of 2,
	// and correctly so.
	if live := len(runningPIDs(t, app)); live != 3 {
		t.Fatalf("%d running, want 3 — two exempt over the ceiling plus the newest", live)
	}
	// A sweep does not shed the exempt pair either.
	app.Evict()
	if !settles(2*time.Second, func() bool { return len(runningPIDs(t, app)) == 2 }) {
		t.Fatalf("after a sweep: %d running, want the 2 exempt", len(runningPIDs(t, app)))
	}
	for _, p := range app.Plugins() {
		if (p.Name == "keep0" || p.Name == "keep1") && !p.Running {
			t.Errorf("%s was evicted by the sweep, but it is exempt", p.Name)
		}
	}
}

// TestWarm_AgeStillReclaimsUnderTheCeiling: the two bounds are one sweep, and age
// reclaims plugins the ceiling had no reason to touch.
func TestWarm_AgeStillReclaimsUnderTheCeiling(t *testing.T) {
	const plugins, warm = 3, 5 // ceiling never reached
	app := warmHost(t, plugins, warm, 40*time.Millisecond)
	ask(t, app, plugins)

	time.Sleep(80 * time.Millisecond) // every one is now past IdleAfter
	if n := app.Evict(); n != plugins {
		t.Fatalf("Evict stopped %d, want all %d on age alone", n, plugins)
	}
	if !settles(3*time.Second, func() bool { return len(runningPIDs(t, app)) == 0 }) {
		t.Fatalf("%d still running", len(runningPIDs(t, app)))
	}
}
