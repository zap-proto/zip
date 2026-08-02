package zip_test

import (
	"os"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// shortDir is t.TempDir() with a SHORT name. A plugin's socket lives inside
// Plugin.Dir, and a unix socket path is capped at 108 bytes by the kernel —
// t.TempDir() spends most of that budget on the test's own name, so a
// descriptively-named test silently fails to bind and reports the child as
// "exited before listening".
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "zs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// A lazy plugin has exactly ONE trigger today: a request reaching one of its
// prefixes. A host that reaches its plugins another way — Hanzo's fleet calls
// app-to-app over each app's own unix socket, which never touches the router —
// has no way to say "bring this one up". Start is that door, and these prove
// it behaves like a door and not like Reload.

func TestStart_BringsUpALazyPluginThatNoRequestHasReached(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: buildPlugin(t, "v1"), Dir: shortDir(t), Lazy: true}, "/v1/demo",
	)))
	t.Cleanup(func() { _ = app.Shutdown() })

	if st := app.Plugins(); st[0].Running {
		t.Fatal("a lazy plugin must not be running before anything asks for it")
	}
	addr, err := app.Start("demo")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if addr == "" {
		t.Fatal("Start returned no address")
	}
	if st := app.Plugins(); !st[0].Running || st[0].PID == 0 {
		t.Fatalf("Start did not bring the plugin up: %+v", st[0])
	}
}

// Idempotence is the whole difference from Reload. Reload on a running plugin
// starts a SECOND child and retires the first; a caller that only wants the
// plugin to exist must not pay a process swap — and must not tear down the
// child that is answering its own in-flight calls.
func TestStart_OnARunningPluginIsANoOp(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: buildPlugin(t, "v1"), Dir: shortDir(t), Lazy: true}, "/v1/demo",
	)))
	t.Cleanup(func() { _ = app.Shutdown() })

	if _, err := app.Start("demo"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := app.Plugins()[0].PID
	for i := 0; i < 5; i++ {
		if _, err := app.Start("demo"); err != nil {
			t.Fatalf("Start #%d: %v", i+2, err)
		}
	}
	st := app.Plugins()[0]
	if st.PID != first {
		t.Fatalf("Start replaced the running child: pid %d → %d", first, st.PID)
	}
	if st.Reloads != 0 {
		t.Fatalf("Start counted %d reloads — it is not a reload", st.Reloads)
	}
}

// Start goes through target(), the same single-flighted path a prefix request
// takes, so a burst of first callers arriving by EITHER door produces exactly
// one child rather than one per caller.
func TestStart_ConcurrentFirstCallersProduceOneChild(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: buildPlugin(t, "v1"), Dir: shortDir(t), Lazy: true}, "/v1/demo",
	)))
	t.Cleanup(func() { _ = app.Shutdown() })

	const n = 12
	var wg sync.WaitGroup
	addrs := make([]string, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func() { defer wg.Done(); addrs[i], errs[i] = app.Start("demo") }()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if addrs[i] != addrs[0] {
			t.Fatalf("caller %d got %q, caller 0 got %q — more than one child", i, addrs[i], addrs[0])
		}
	}
	if st := app.Plugins()[0]; st.Reloads != 0 {
		t.Fatalf("%d concurrent Starts caused %d reloads", n, st.Reloads)
	}
}

// Unload must stick. A deliberate stop that the next caller silently undoes is
// not a stop, and for a LAZY plugin — the affordable default — every caller is
// a potential resurrection.
func TestStart_WillNotResurrectAnUnloadedPlugin(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: buildPlugin(t, "v1"), Dir: shortDir(t), Lazy: true}, "/v1/demo",
	)))
	t.Cleanup(func() { _ = app.Shutdown() })

	if _, err := app.Start("demo"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := app.Unload("demo"); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if _, err := app.Start("demo"); err == nil {
		t.Fatal("Start resurrected an unloaded plugin")
	}
	if st := app.Plugins()[0]; st.Running {
		t.Fatal("unloaded plugin is running again")
	}
}

func TestStart_UnknownPluginIsAnError(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if _, err := app.Start("nope"); err == nil {
		t.Fatal("Start on an unknown plugin must fail")
	}
}
