package zip_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Shutdown asks each child to exit and waits for it, so a child that takes time
// on the way out — closing and shipping its stores — gets to finish. A kill
// would end it before it wrote the file.
func TestShutdown_TermsChildBeforeKilling(t *testing.T) {
	bin := buildPlugin(t, "v1")
	mark := filepath.Join(t.TempDir(), "drained")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: bin, Env: []string{
		"TESTPLUGIN_ON_TERM=" + mark, "TESTPLUGIN_TERM_DELAY=300ms",
	}}, "/v1/demo")))
	version(t, app)

	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	b, err := os.ReadFile(mark)
	if err != nil || string(b) != "drained" {
		t.Fatalf("child did not finish its shutdown before the host returned: %q, %v", b, err)
	}
}

// A child that ignores SIGTERM is killed once its grace is spent, and the host
// does not wait past it.
func TestShutdown_KillsChildAfterGrace(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: bin, Grace: 300 * time.Millisecond,
		Env: []string{"TESTPLUGIN_TERM=ignore"}}, "/v1/demo")))
	version(t, app)
	pid := pidOf(app, "demo")
	if pid == 0 {
		t.Fatal("demo is not running")
	}

	start := time.Now()
	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	took := time.Since(start)
	if took < 300*time.Millisecond || took > 5*time.Second {
		t.Fatalf("Shutdown took %s, want the 300ms grace and then a kill", took)
	}
	if alive(pid) {
		t.Fatalf("child %d survived its host", pid)
	}
}

// Children drain together: three that each take 600ms on the way out cost the
// host one grace, not three.
func TestShutdown_ChildrenDrainTogether(t *testing.T) {
	bin := buildPlugin(t, "v1")
	dir := t.TempDir()

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	for i := range 3 {
		n := strconv.Itoa(i)
		app.Use(must(zip.Load(zip.Plugin{Name: "demo" + n, Bin: bin, Env: []string{
			"TESTPLUGIN_ON_TERM=" + filepath.Join(dir, n), "TESTPLUGIN_TERM_DELAY=600ms",
		}}, "/v1/demo"+n)))
	}
	for i := range 3 {
		if pidOf(app, "demo"+strconv.Itoa(i)) == 0 {
			t.Fatalf("demo%d is not running", i)
		}
	}

	start := time.Now()
	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("Shutdown took %s; three 600ms drains ran one after another", took)
	}
	for i := range 3 {
		if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(i))); err != nil {
			t.Fatalf("child %d did not finish its shutdown: %v", i, err)
		}
	}
}

// The wait for the children is bounded by their grace, not by the context a
// shutdown was given: that context is usually spent on the drain, and a host
// that returned before its children would be torn down under them.
func TestShutdown_OutlivesAnEndedContext(t *testing.T) {
	bin := buildPlugin(t, "v1")
	mark := filepath.Join(t.TempDir(), "drained")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: bin, Env: []string{
		"TESTPLUGIN_ON_TERM=" + mark, "TESTPLUGIN_TERM_DELAY=300ms",
	}}, "/v1/demo")))
	version(t, app)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = app.ShutdownWithContext(ctx)
	if _, err := os.Stat(mark); err != nil {
		t.Fatalf("the host returned before its child finished: %v", err)
	}
}
