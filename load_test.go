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
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// buildPlugin compiles internal/testplugin with version stamped in, and returns
// the binary's bytes — exactly what a host would go:embed. Takes a testing.TB
// so the benchmarks build their subject the same way the tests do.
func buildPlugin(t testing.TB, version string) []byte {
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
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: bin}, "/v1/demo")))
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
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: v1}, "/v1/demo")))
	defer func() { _ = app.Shutdown() }()

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("before reload: body=%q, want v1", got)
	}

	if err := app.Reload("demo", zip.Plugin{Bin: v2}); err != nil {
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
		if err := app.Reload("demo", zip.Plugin{Bin: bin}); err != nil {
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
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: v1}, "/v1/demo")))
	defer func() { _ = app.Shutdown() }()

	// Not a binary at all — exec fails, or it dies immediately.
	if err := app.Reload("demo", zip.Plugin{Bin: []byte("#!/bin/false\n")}); err == nil {
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
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: v1}, "/v1/demo")))
	defer func() { _ = app.Shutdown() }()

	if err := app.Unload("demo"); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 503 {
		t.Fatalf("after Unload: status %d, want 503 (route present, no instance)", status)
	}

	if err := app.Reload("demo", zip.Plugin{}); err != nil {
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
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Addr: sock}, "/v1/demo")))
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
	app.Use(must(zip.Load(zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Sum: hex.EncodeToString(sum[:]), Dir: cache,
	}, "/v1/demo")))
	defer func() { _ = app.Shutdown() }()

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("body=%q, want the installed plugin's response", got)
	}

	// A second load at the same digest must reuse the cached binary rather than
	// download again — that is what makes a restart offline.
	app2 := zip.New(zip.Config{AppName: "host2", DisableStartupMessage: true})
	app2.Use(must(zip.Load(zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Sum: hex.EncodeToString(sum[:]), Dir: cache,
	}, "/v1/demo")))
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

	// The verification is part of BUILDING the definition, so the refusal comes
	// from Load itself — there is nothing to compose and nothing to build.
	_, err := zip.Load(zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Dir: t.TempDir(),
		Sum: strings.Repeat("00", 32), // wrong on purpose
	}, "/v1/demo")
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
	_, err := zip.Load(zip.Plugin{Name: "demo", URL: "https://example.test/x"}, "/v1/demo")
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
	app.Use(
		must(zip.Load(zip.Plugin{Name: "embedded", Bin: v1, Dir: cache}, "/v1/embedded")),
		must(zip.Load(zip.Plugin{
			Name: "installed", URL: srv.URL + "/x", Sum: hex.EncodeToString(sum[:]), Dir: cache,
		}, "/v1/installed")),
	)
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
	if err := app.Reload("embedded", zip.Plugin{Bin: v2}); err != nil {
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
	app.Use(must(zip.Load(zip.Plugin{
		Name: "demo", Bin: bin, Dir: t.TempDir(),
	}, "/v1/demo")))

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
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: bin, Dir: t.TempDir()},
		"/v1/demo", "/v1/other",
	)))
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

	// And the status report must name BOTH. Reporting only the first understates
	// what goes dark when this plugin does, which is the one question a fleet
	// view exists to answer.
	got := app.Plugins()
	if len(got) != 1 {
		t.Fatalf("Plugins() returned %d, want 1", len(got))
	}
	if want := []string{"/v1/demo", "/v1/other"}; !slices.Equal(got[0].Prefixes, want) {
		t.Fatalf("Prefixes = %v, want %v", got[0].Prefixes, want)
	}
	if got[0].Prefix != "/v1/demo" {
		t.Fatalf("Prefix = %q, want the first prefix", got[0].Prefix)
	}
}

// TestPlugins_RemotePrefixes proves the same for a plugin this host did NOT
// start: an Addr mount records every subtree it answers, so a fleet view of a
// split deploy reports the whole reachable surface rather than one prefix of it.
func TestPlugins_RemotePrefixes(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "remote", Addr: "127.0.0.1:1"}, "/v1/remote", "/v1/legacy",
	)))
	defer func() { _ = app.Shutdown() }()

	got := app.Plugins()
	if len(got) != 1 || got[0].Source != "remote" {
		t.Fatalf("Plugins() = %+v, want one remote", got)
	}
	if want := []string{"/v1/remote", "/v1/legacy"}; !slices.Equal(got[0].Prefixes, want) {
		t.Fatalf("Prefixes = %v, want %v", got[0].Prefixes, want)
	}
}

// TestLoad_NoPrefix proves Load refuses to mount a plugin nowhere.
func TestLoad_NoPrefix(t *testing.T) {
	_, err := zip.Load(zip.Plugin{Name: "demo", Path: "/nonexistent"})
	if err == nil || !strings.Contains(err.Error(), "at least one prefix") {
		t.Fatalf("err = %v, want a refusal naming the missing prefix", err)
	}
}

// TestPlugins_Usage proves per-plugin resource accounting is real. This is the
// observability a monolith cannot offer at any price: one process has one RSS
// and one CPU clock, and nothing inside it can be attributed to a subsystem.
// Running a service as its own process turns "which app is eating the memory"
// into a question with an exact, kernel-measured answer.
func TestPlugins_Usage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("resource accounting reads /proc")
	}
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: bin, Dir: t.TempDir()}, "/v1/demo",
	)))
	defer func() { _ = app.Shutdown() }()

	// Drive it so there is CPU to account for.
	for i := 0; i < 20; i++ {
		if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 200 {
			t.Fatalf("request %d: status %d", i, status)
		}
	}

	u := app.Plugins()[0].Usage
	if u.RSS <= 0 {
		t.Fatalf("RSS = %d, want a real resident set for a running child", u.RSS)
	}
	if u.Threads <= 0 {
		t.Fatalf("Threads = %d, want at least one", u.Threads)
	}
	if u.FDs <= 0 {
		t.Fatalf("FDs = %d, want at least the socket", u.FDs)
	}
	// A Go runtime always has several threads and holds more than a trivial
	// resident set, so these also guard against parsing the wrong /proc fields.
	if u.Threads > 10000 || u.RSS > 1<<40 {
		t.Fatalf("implausible usage %+v — likely misparsed /proc fields", u)
	}
	t.Logf("plugin usage: cpu=%v rss=%.1fMB threads=%d fds=%d",
		u.CPU, float64(u.RSS)/(1<<20), u.Threads, u.FDs)
}

// TestPlugins_SurvivesPanic proves the central safety claim of running a plugin
// as its own process: a panic in the plugin cannot take the host down, the host
// NOTICES rather than serving from a corpse, and the plugin comes back on its
// own.
//
// Without a supervisor the failure is silent and permanent — the child is gone
// but the mount still points at it, so every request gets a connection error
// dressed as a 502 and the status keeps reporting Running.
func TestPlugins_SurvivesPanic(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: bin, Dir: t.TempDir()}, "/v1/demo",
	)))
	defer func() { _ = app.Shutdown() }()

	first := app.Plugins()[0].PID
	if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 200 {
		t.Fatalf("before crash: status %d", status)
	}

	// Panic it on a goroutine — unrecoverable, so the process really dies.
	if status, _ := call(t, app, "GET", "/v1/demo/crash", ""); status != 200 {
		t.Fatalf("crash trigger: status %d", status)
	}

	// THE HOST MUST SURVIVE. If a plugin panic could kill the host, none of
	// this architecture is worth anything.
	deadline := time.Now().Add(20 * time.Second)
	var restarted bool
	for time.Now().Before(deadline) {
		ps := app.Plugins()
		if len(ps) != 1 {
			t.Fatalf("plugin vanished from status: %+v", ps)
		}
		if ps[0].Restarts > 0 && ps[0].Running && ps[0].PID != first {
			restarted = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !restarted {
		t.Fatalf("plugin did not come back: %+v", app.Plugins()[0])
	}

	// And it serves again, on the same routes, with no re-registration.
	if status, body := call(t, app, "GET", "/v1/demo/version", ""); status != 200 ||
		!strings.Contains(body, `"version":"v1"`) {
		t.Fatalf("after restart: status=%d body=%q", status, body)
	}
	t.Logf("survived: pid %d -> %d, restarts=%d", first, app.Plugins()[0].PID, app.Plugins()[0].Restarts)
}

// TestLoad_Lazy proves a lazy plugin registers its routes at Load but does not
// start until a request actually reaches one. This is what makes many plugins
// affordable: a host composing dozens of services eagerly pays a process, a
// resident set and a startup for every one of them at boot, for a set that is
// mostly idle.
func TestLoad_Lazy(t *testing.T) {
	bin := buildPlugin(t, "v1")

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(
		zip.Plugin{Name: "demo", Bin: bin, Dir: t.TempDir(), Lazy: true}, "/v1/demo",
	)))
	defer func() { _ = app.Shutdown() }()

	// Registered but not running — that distinction is the whole feature.
	ps := app.Plugins()
	if len(ps) != 1 {
		t.Fatalf("lazy plugin not registered: %+v", ps)
	}
	if ps[0].Running || ps[0].PID != 0 {
		t.Fatalf("lazy plugin started at Load: %+v — nothing had asked for it yet", ps[0])
	}

	// The first request brings it up and is served, not failed. A lazy plugin
	// that 503'd its own first request would be useless.
	status, body := call(t, app, "GET", "/v1/demo/version", "")
	if status != 200 || !strings.Contains(body, `"version":"v1"`) {
		t.Fatalf("first request to a lazy plugin: status=%d body=%q", status, body)
	}

	after := app.Plugins()[0]
	if !after.Running || after.PID == 0 {
		t.Fatalf("still not running after a request: %+v", after)
	}
	// And it stays up — the second request must not spawn another child.
	if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 200 {
		t.Fatalf("second request: status %d", status)
	}
	if pid := app.Plugins()[0].PID; pid != after.PID {
		t.Fatalf("pid changed %d -> %d — the plugin was started twice", after.PID, pid)
	}
	t.Logf("lazy: not running at Load, started on first request as pid %d", after.PID)
}

// TestReload_PinAndRollbackByDigest proves the two operations a control plane
// needs and Reload alone cannot express: moving a plugin to a DIFFERENT
// artifact, and rolling it back to one this host already ran — offline. The
// origin is shut down before the rollback, so a rollback that touched the
// network could not pass.
func TestReload_PinAndRollbackByDigest(t *testing.T) {
	v1, v2 := buildPlugin(t, "v1"), buildPlugin(t, "v2")
	s1, s2 := sha256.Sum256(v1), sha256.Sum256(v2)
	d1, d2 := hex.EncodeToString(s1[:]), hex.EncodeToString(s2[:])

	bits := map[string][]byte{"/v1": v1, "/v2": v2}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bits[r.URL.Path])
	}))

	cache := t.TempDir()
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(zip.Plugin{
		Name: "demo", URL: srv.URL + "/v1", Sum: d1, Dir: cache,
	}, "/v1/demo")))
	defer func() { _ = app.Shutdown() }()

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("body=%q, want v1", got)
	}
	was := app.Plugins()[0]

	// Pin forward to a version this host has never seen.
	if err := app.Reload("demo", zip.Plugin{URL: srv.URL + "/v2", Sum: d2}); err != nil {
		t.Fatalf("ReloadTo(v2): %v", err)
	}
	if got := version(t, app); !strings.Contains(got, `"version":"v2"`) {
		t.Fatalf("after pin: body=%q, want v2 answering", got)
	}
	now := app.Plugins()[0]
	if now.PID == was.PID {
		t.Fatal("pid did not change — no swap happened")
	}
	// The reported version must be the bits running, not the bits loaded.
	if now.Version != d2 {
		t.Fatalf("Version = %q, want the v2 digest %q", now.Version, d2)
	}

	// Everything after this point must come off local disk.
	srv.Close()

	if err := app.Reload("demo", zip.Plugin{URL: srv.URL + "/v1", Sum: d1}); err != nil {
		t.Fatalf("rollback by digest hit the network: %v", err)
	}
	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("after rollback: body=%q, want v1", got)
	}
	if v := app.Plugins()[0].Version; v != d1 {
		t.Fatalf("Version = %q, want the v1 digest %q", v, d1)
	}
	t.Logf("pinned v1->v2->v1 by digest; rollback served from cache with the origin down")
}

// TestReload_RefusesUnverifiedURL proves the control plane cannot be talked
// into running an unpinned artifact by going through reload instead of Load.
func TestReload_RefusesUnverifiedURL(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: buildPlugin(t, "v1")}, "/v1/demo")))
	defer func() { _ = app.Shutdown() }()

	err := app.Reload("demo", zip.Plugin{URL: "https://example.test/x"})
	if err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("err = %v, want a refusal naming the unverified download", err)
	}
	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("body=%q, want v1 still serving after the refusal", got)
	}
}

// TestReload_RefusesRemote proves a mount this host did not start reports why
// rather than failing obscurely inside exec.
func TestReload_RefusesRemote(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(zip.Plugin{Name: "demo", Addr: "127.0.0.1:9"}, "/v1/demo")))
	err := app.Reload("demo", zip.Plugin{Bin: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "remote Addr") {
		t.Fatalf("err = %v, want a refusal naming the remote mount", err)
	}
}

// TestUnload_LazyStaysDown is the one that matters in production: a host
// composing many services runs nearly all of them Lazy, and a lazy plugin is
// started BY a request. Without a disabled flag the very next request undoes
// the Unload, so "disable" would silently not stick for exactly the plugins
// most hosts run.
func TestUnload_LazyStaysDown(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(must(zip.Load(zip.Plugin{
		Name: "demo", Bin: buildPlugin(t, "v1"), Lazy: true,
	}, "/v1/demo")))
	defer func() { _ = app.Shutdown() }()

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("body=%q, want the lazy plugin started by the first request", got)
	}
	if err := app.Unload("demo"); err != nil {
		t.Fatalf("Unload: %v", err)
	}

	// The route must stay registered and answer 503 — not 404, and not restart.
	for i := 0; i < 3; i++ {
		if status, _ := call(t, app, "GET", "/v1/demo/version", ""); status != 503 {
			t.Fatalf("request %d after Unload: status %d, want 503", i, status)
		}
	}
	st := app.Plugins()[0]
	if st.Running || !st.Disabled {
		t.Fatalf("status running=%v disabled=%v, want running=false disabled=true", st.Running, st.Disabled)
	}

	// Reload is what re-enables it, and it must come back on the same route.
	if err := app.Reload("demo", zip.Plugin{}); err != nil {
		t.Fatalf("Reload after Unload: %v", err)
	}
	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("after re-enable: body=%q, want v1 serving again", got)
	}
	if app.Plugins()[0].Disabled {
		t.Fatal("still marked disabled after a successful Reload")
	}
}
