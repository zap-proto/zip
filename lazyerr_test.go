package zip_test

import (
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestALazyPluginThatCannotStartSaysWhy is the gap that hid a nine-surface outage.
//
// An EAGER plugin that will not boot fails at Load, where the caller sees the
// error. A LAZY one cannot: its mount succeeds with no process behind it — that is
// what Lazy means — and the start happens on the first request, inside the child,
// long after anyone was watching. The route then answers 503 "no instance running"
// for that request and every later one.
//
// Running=false was the only trace, and it cannot tell the two apart: a lazy plugin
// nobody has asked for yet reports exactly the same thing as one whose every start
// dies. So a host could report itself healthy — api.hanzo.ai answered
// {"status":"ok"} — while nine of its surfaces were permanently 503.
func TestALazyPluginThatCannotStartSaysWhy(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		// A binary that is not there. Any start failure would do; this one is just
		// the cheapest to be certain of.
		zip.Plugin{Name: "demo", Path: "/nonexistent/plugin-binary", Dir: sockDir(t), Lazy: true},
		"/v1/demo",
	)))
	defer func() { _ = app.Shutdown() }()

	// BEFORE the first request the plugin is legitimately not running and there is
	// nothing to explain. Reporting an error here would make "cold" look like
	// "broken" on every host that has not been asked yet.
	if got := app.Plugins()[0]; got.Running || got.Error != "" {
		t.Fatalf("a lazy plugin that has never been asked reports %+v; "+
			"cold is not an error and must not read as one", got)
	}

	// The request that triggers the start, and fails.
	if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 503 {
		t.Fatalf("a plugin that cannot start answered %d, want 503", status)
	}

	// AND NOW IT SAYS WHY. This is the whole point: the 503 is answered again for
	// every later request, so the reason has to outlive the log line that carried it.
	got := app.Plugins()[0]
	if got.Running {
		t.Fatalf("plugin reports Running after a failed start: %+v", got)
	}
	if got.Error == "" {
		t.Fatal("a plugin whose start failed reports no error — " +
			"Running=false alone cannot separate 'never asked' from 'will not come up', " +
			"which is how a dead surface reads as a healthy host")
	}
	if !strings.Contains(got.Error, "demo") && !strings.Contains(got.Error, "plugin-binary") &&
		!strings.Contains(got.Error, "no such file") && !strings.Contains(got.Error, "exec") {
		t.Errorf("Error = %q — it names neither the plugin nor the failure, "+
			"so it is not something an operator can act on", got.Error)
	}
}
