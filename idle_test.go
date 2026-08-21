package zip_test

import (
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestIdle_EvictsAndComesBack is the whole contract in one pass: a lazy plugin
// that has gone unused is stopped, and the next request starts it again.
//
// The second half matters more than the first. Stopping a process is easy; the
// reason this is eviction and not Unload is that the route still works
// afterwards, and the caller cannot tell except by the pid.
func TestIdle_EvictsAndComesBack(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{
			Name: "demo", Bin: bin, Dir: sockDir(t), Lazy: true,
			IdleAfter: 50 * time.Millisecond,
		}, "/v1/demo",
	)))
	defer func() { _ = app.Shutdown() }()

	if status, body := call(t, app, "GET", "/v1/demo/version", ""); status != 200 {
		t.Fatalf("first request: status=%d body=%q", status, body)
	}
	first := app.Plugins()[0]
	if !first.Running || first.PID == 0 {
		t.Fatalf("plugin not running after a served request: %+v", first)
	}

	// Fresh from a request, so it is NOT idle. An evictor that stops a plugin
	// that just served would be a request-latency bug that only shows in prod.
	if n := app.Evict(); n != 0 {
		t.Fatalf("evicted %d plugins that had just served", n)
	}
	if !app.Plugins()[0].Running {
		t.Fatal("plugin stopped despite serving a moment ago")
	}

	time.Sleep(80 * time.Millisecond)
	if n := app.Evict(); n != 1 {
		t.Fatalf("Evict stopped %d, want 1 after idling past IdleAfter", n)
	}

	// retire() drains asynchronously; with Drain unset there is no grace to
	// wait out, so this settles immediately in practice.
	if !settles(2*time.Second, func() bool { return !app.Plugins()[0].Running }) {
		t.Fatalf("plugin still running after eviction: %+v", app.Plugins()[0])
	}

	// The route still answers, and answers from a DIFFERENT process.
	status, body := call(t, app, "GET", "/v1/demo/version", "")
	if status != 200 {
		t.Fatalf("request after eviction: status=%d body=%q — eviction must not take the route down", status, body)
	}
	second := app.Plugins()[0]
	if !second.Running || second.PID == 0 {
		t.Fatalf("plugin did not restart on demand: %+v", second)
	}
	if second.PID == first.PID {
		t.Fatalf("same pid %d before and after eviction — nothing was actually stopped", first.PID)
	}
}

// TestIdle_ZeroNeverEvicts is the default, and the setting an always-hot plugin
// wants: identity or config that every other call goes through must never make
// a caller pay a cold start.
func TestIdle_ZeroNeverEvicts(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: bin, Dir: sockDir(t), Lazy: true}, // IdleAfter unset
		"/v1/demo",
	)))
	defer func() { _ = app.Shutdown() }()

	if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 200 {
		t.Fatal("first request failed")
	}
	pid := app.Plugins()[0].PID

	time.Sleep(60 * time.Millisecond)
	if n := app.Evict(); n != 0 {
		t.Fatalf("evicted %d with IdleAfter unset — zero must mean never", n)
	}
	if got := app.Plugins()[0]; !got.Running || got.PID != pid {
		t.Fatalf("plugin disturbed despite IdleAfter unset: %+v (was pid %d)", got, pid)
	}
}

// TestIdle_EagerIsNeverEvicted: an eager plugin was started at Load on purpose.
// Stopping it would contradict the instruction that started it, and its next
// request would pay a start it was configured never to pay.
func TestIdle_EagerIsNeverEvicted(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{
			Name: "demo", Bin: bin, Dir: sockDir(t), // Lazy false
			IdleAfter: 10 * time.Millisecond,
		}, "/v1/demo",
	)))
	defer func() { _ = app.Shutdown() }()

	pid := app.Plugins()[0].PID
	if pid == 0 {
		t.Fatal("eager plugin did not start at Load")
	}
	// Serve one request first. Without it lastUse stays zero and evictIfIdle
	// returns on that check before it ever consults Lazy — the test would pass
	// whether or not the eager guard exists, which a mutation proved it did.
	if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 200 {
		t.Fatal("eager plugin did not serve")
	}
	time.Sleep(40 * time.Millisecond)
	if n := app.Evict(); n != 0 {
		t.Fatalf("evicted %d eager plugins — IdleAfter applies to lazy only", n)
	}
	if got := app.Plugins()[0]; !got.Running || got.PID != pid {
		t.Fatalf("eager plugin stopped: %+v (was pid %d)", got, pid)
	}
}

// TestIdle_ReapRunsAndStops covers the ticker wrapper, including that its
// stop function is idempotent — a host calling it twice (defer plus an explicit
// shutdown path) must not panic on a closed channel.
func TestIdle_ReapRunsAndStops(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{
			Name: "demo", Bin: bin, Dir: sockDir(t), Lazy: true,
			IdleAfter: 20 * time.Millisecond,
		}, "/v1/demo",
	)))
	defer func() { _ = app.Shutdown() }()

	if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 200 {
		t.Fatal("first request failed")
	}
	stop := app.Reap(10 * time.Millisecond)
	if !settles(3*time.Second, func() bool { return !app.Plugins()[0].Running }) {
		t.Fatal("Reap never evicted an idle plugin")
	}
	stop()
	stop() // idempotent
}

func settles(limit time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return ok()
}
