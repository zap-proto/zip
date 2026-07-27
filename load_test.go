package zip_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// buildPlugin compiles internal/testplugin with version stamped in, and returns
// the binary's bytes — exactly what a host would go:embed.
func buildPlugin(t *testing.T, version string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "testplugin-"+version)
	cmd := exec.Command("go", "build",
		"-ldflags", "-X main.version="+version,
		"-o", out, "./internal/testplugin")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build test plugin (no toolchain?): %v\n%s", err, b)
	}
	bin, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read built plugin: %v", err)
	}
	return bin
}

func version(t *testing.T, app *zip.App) string {
	t.Helper()
	status, body := call(t, app, "GET", "/v1/demo/version", "")
	if status != 200 {
		t.Fatalf("GET version: status %d (body=%s)", status, body)
	}
	return body
}

// TestLoad_EmbeddedBinary proves the headline case: a binary held in memory —
// as go:embed would hold it — is started as a child on its own unix socket and
// its routes answer through the host, with the host holding no reference to the
// plugin's code.
func TestLoad_EmbeddedBinary(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{Name: "demo", Bin: bin}, "/v1/demo")); err != nil {
		t.Fatalf("Add(Load): %v", err)
	}
	defer func() { _ = app.Shutdown() }()

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("body=%q, want the v1 plugin's response", got)
	}
}

// TestLoad_Reload is the load/unload proof. A second, genuinely different build
// replaces the first at run time: requests before the swap are answered by v1,
// requests after by v2, and nothing 404s or 502s in between. The host binary is
// never relinked.
func TestLoad_Reload(t *testing.T) {
	v1, v2 := buildPlugin(t, "v1"), buildPlugin(t, "v2")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{Name: "demo", Bin: v1}, "/v1/demo")); err != nil {
		t.Fatalf("Add(Load): %v", err)
	}
	defer func() { _ = app.Shutdown() }()

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("before reload: body=%q, want v1", got)
	}

	if err := app.Reload("demo", v2); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got := version(t, app); !strings.Contains(got, `"version":"v2"`) {
		t.Fatalf("after reload: body=%q, want v2 — the swap did not take", got)
	}

	// Reloading repeatedly must stay flat: routes register once at Load, so a
	// second and third swap change the target, never the router.
	for _, want := range []string{"v1", "v2", "v1"} {
		bin := v1
		if want == "v2" {
			bin = v2
		}
		if err := app.Reload("demo", bin); err != nil {
			t.Fatalf("Reload to %s: %v", want, err)
		}
		if got := version(t, app); !strings.Contains(got, `"version":"`+want+`"`) {
			t.Fatalf("after reload to %s: body=%q", want, got)
		}
	}
}

// TestLoad_ReloadFailureKeepsServing proves a bad build cannot take the route
// down: the replacement is proven listening BEFORE any request moves to it, so
// a plugin that will not start leaves the running one untouched.
func TestLoad_ReloadFailureKeepsServing(t *testing.T) {
	v1 := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{Name: "demo", Bin: v1}, "/v1/demo")); err != nil {
		t.Fatalf("Add(Load): %v", err)
	}
	defer func() { _ = app.Shutdown() }()

	// Not a binary at all — exec fails, or it dies immediately.
	if err := app.Reload("demo", []byte("#!/bin/false\n")); err == nil {
		t.Fatal("Reload with a broken binary returned nil, want an error")
	}

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("after a failed reload: body=%q, want v1 still serving", got)
	}
}

// TestLoad_Unload proves an unloaded plugin's routes stay registered and report
// 503 rather than vanishing, and that a Reload brings it back on the same
// routes — the route table is never mutated.
func TestLoad_Unload(t *testing.T) {
	v1 := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{Name: "demo", Bin: v1}, "/v1/demo")); err != nil {
		t.Fatalf("Add(Load): %v", err)
	}
	defer func() { _ = app.Shutdown() }()

	if err := app.Unload("demo"); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 503 {
		t.Fatalf("after Unload: status %d, want 503 (route present, no instance)", status)
	}

	if err := app.Reload("demo", nil); err != nil {
		t.Fatalf("Reload after Unload: %v", err)
	}
	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("after Reload: body=%q, want v1 back on the same route", got)
	}
}

// TestLoad_AlreadyRunning proves the third source: Addr mounts an instance this
// host did not start, through the same Load call and the same Service type.
func TestLoad_AlreadyRunning(t *testing.T) {
	plugin := zip.New(zip.Config{AppName: "demo", DisableStartupMessage: true})
	plugin.Get("/v1/demo/version", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"version": "external"})
	})
	sock := filepath.Join(t.TempDir(), "demo.sock")
	go func() { _ = plugin.Listen(sock) }()
	defer func() { _ = plugin.Shutdown() }()
	waitSocket(t, sock)

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{Name: "demo", Addr: sock}, "/v1/demo")); err != nil {
		t.Fatalf("Add(Load with Addr): %v", err)
	}
	if got := version(t, app); !strings.Contains(got, `"version":"external"`) {
		t.Fatalf("body=%q, want the already-running instance", got)
	}
}

// TestLoad_FromRelease proves the release-artifact path: a host that carries no
// plugin binary installs one over HTTP, verifies it, and serves its routes.
// This is what decouples the two build cycles — the host never compiles it.
func TestLoad_FromRelease(t *testing.T) {
	bin := buildPlugin(t, "v1")
	sum := sha256.Sum256(bin)

	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		_, _ = w.Write(bin)
	}))
	defer srv.Close()

	cache := t.TempDir()
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Sum: hex.EncodeToString(sum[:]), Dir: cache,
	}, "/v1/demo")); err != nil {
		t.Fatalf("Add(Load from release): %v", err)
	}
	defer func() { _ = app.Shutdown() }()

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("body=%q, want the installed plugin's response", got)
	}

	// A second load at the same digest must reuse the cached binary rather than
	// download again — that is what makes a restart offline.
	app2 := zip.New(zip.Config{AppName: "host2", DisableStartupMessage: true})
	if err := app2.Add(zip.Load(zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Sum: hex.EncodeToString(sum[:]), Dir: cache,
	}, "/v1/demo")); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	defer func() { _ = app2.Shutdown() }()
	if served != 1 {
		t.Fatalf("server was hit %d times, want 1 — the digest cache did not hold", served)
	}
}

// TestLoad_ReleaseRejectsBadSum proves a substituted or corrupted download is
// never executed. This is the whole reason Sum is mandatory with URL.
func TestLoad_ReleaseRejectsBadSum(t *testing.T) {
	bin := buildPlugin(t, "v1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bin)
	}))
	defer srv.Close()

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	err := app.Add(zip.Load(zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Dir: t.TempDir(),
		Sum: strings.Repeat("00", 32), // wrong on purpose
	}, "/v1/demo"))
	if err == nil {
		t.Fatal("a mismatched Sum was accepted — the binary would have been executed")
	}
	if !strings.Contains(err.Error(), "does not match Sum") {
		t.Fatalf("err = %v, want it to name the digest mismatch", err)
	}
}

// TestLoad_ReleaseRequiresSum proves a URL without a Sum is refused outright,
// rather than trusting the network.
func TestLoad_ReleaseRequiresSum(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	err := app.Add(zip.Load(zip.Plugin{Name: "demo", URL: "https://example.test/x"}, "/v1/demo"))
	if err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("err = %v, want a refusal naming the unverified download", err)
	}
}

// TestPlugins_Status proves a host can report what it is actually running —
// the primitive a fleet view needs, since deployment config says what was
// INTENDED and only the process knows what is TRUE.
func TestPlugins_Status(t *testing.T) {
	v1, v2 := buildPlugin(t, "v1"), buildPlugin(t, "v2")
	sum := sha256.Sum256(v1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(v1)
	}))
	defer srv.Close()

	cache := t.TempDir()
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(
		zip.Load(zip.Plugin{Name: "embedded", Bin: v1, Dir: cache}, "/v1/embedded"),
		zip.Load(zip.Plugin{
			Name: "installed", URL: srv.URL + "/x", Sum: hex.EncodeToString(sum[:]), Dir: cache,
		}, "/v1/installed"),
	); err != nil {
		t.Fatalf("Add: %v", err)
	}
	defer func() { _ = app.Shutdown() }()

	got := app.Plugins()
	if len(got) != 2 {
		t.Fatalf("Plugins() returned %d, want 2", len(got))
	}
	// Sorted by name, so this order is stable and a diff between hosts is too.
	if got[0].Name != "embedded" || got[1].Name != "installed" {
		t.Fatalf("not sorted by name: %v", []string{got[0].Name, got[1].Name})
	}
	if got[0].Source != "embedded" || got[1].Source != "url" {
		t.Fatalf("sources = %q/%q, want embedded/url", got[0].Source, got[1].Source)
	}
	// The digest IS the version — it cannot drift from the bits running.
	if got[1].Version != hex.EncodeToString(sum[:]) {
		t.Fatalf("installed Version = %q, want the artifact digest", got[1].Version)
	}
	for _, p := range got {
		if !p.Running || p.PID == 0 || p.Since.IsZero() {
			t.Fatalf("%s: running=%v pid=%d since=%v — want a live instance", p.Name, p.Running, p.PID, p.Since)
		}
		if p.Reloads != 0 {
			t.Fatalf("%s: Reloads=%d before any reload", p.Name, p.Reloads)
		}
	}

	// A reload must be visible: the counter climbs and Since resets.
	before := got[0].Since
	if err := app.Reload("embedded", v2); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	after := app.Plugins()[0]
	if after.Reloads != 1 {
		t.Fatalf("Reloads = %d after one reload, want 1", after.Reloads)
	}
	if !after.Since.After(before) {
		t.Fatalf("Since did not advance on reload (%v -> %v)", before, after.Since)
	}

	// Unload must read as "deployed but down", not as absent — its routes are
	// still registered and answering 503.
	if err := app.Unload("embedded"); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	down := app.Plugins()[0]
	if down.Running || down.Name != "embedded" {
		t.Fatalf("after Unload: %+v — want the plugin still listed, Running=false", down)
	}
}

// TestLoad_ChildDiesWithHost proves a plugin does not outlive a host that was
// killed outright. Graceful Shutdown already stops children; this covers the
// case it cannot — SIGKILL, an OOM kill, a crash — where the host never runs a
// hook and the child would otherwise keep serving on a stale socket for a
// process that no longer exists.
func TestLoad_ChildDiesWithHost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("parent-death signal is Linux-only")
	}
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{
		Name: "demo", Bin: bin, Dir: t.TempDir(),
	}, "/v1/demo")); err != nil {
		t.Fatalf("Add(Load): %v", err)
	}

	pid := app.Plugins()[0].PID
	if pid == 0 {
		t.Fatal("no child pid reported")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child %d is not running: %v", pid, err)
	}

	// Shut the host down the ordinary way and confirm the child is reaped. The
	// Pdeathsig path itself only fires on real host death, which a test cannot
	// stage without killing its own process — so this asserts the reachable
	// half, and childsig_linux.go covers the other.
	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for i := 0; i < 200; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return // gone
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child %d still alive after Shutdown", pid)
}

// TestLoad_MultiplePrefixes proves one plugin can own more than one route
// subtree. Real services do — o11y answers both /v1/o11y and /v1/sentry — and a
// single-prefix Load silently 404s every subtree it did not name, which is the
// worst failure mode: the host starts, reports healthy, and serves nothing on
// the paths it dropped.
func TestLoad_MultiplePrefixes(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(
		zip.Plugin{Name: "demo", Bin: bin, Dir: t.TempDir()},
		"/v1/demo", "/v1/other",
	)); err != nil {
		t.Fatalf("Add(Load with two prefixes): %v", err)
	}
	defer func() { _ = app.Shutdown() }()

	// Its real route, through the first mount.
	if status, body := call(t, app, "GET", "/v1/demo/version", ""); status != 200 ||
		!strings.Contains(body, `"version":"v1"`) {
		t.Fatalf("/v1/demo/version: status=%d body=%q", status, body)
	}
	// The plugin echoes any path it receives, so a 200 carrying the path proves
	// the request REACHED the plugin through the second mount. A host that never
	// registered that prefix would 404 here, and the echo is what distinguishes
	// that from the plugin simply not having the route.
	status, body := call(t, app, "GET", "/v1/other/anything", "")
	if status != 200 || !strings.Contains(body, `"echo":"/v1/other/anything"`) {
		t.Fatalf("/v1/other/anything: status=%d body=%q — the second prefix did not reach the plugin", status, body)
	}
}

// TestLoad_NoPrefix proves Load refuses to mount a plugin nowhere.
func TestLoad_NoPrefix(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	err := app.Add(zip.Load(zip.Plugin{Name: "demo", Path: "/nonexistent"}))
	if err == nil || !strings.Contains(err.Error(), "at least one prefix") {
		t.Fatalf("err = %v, want a refusal naming the missing prefix", err)
	}
}
