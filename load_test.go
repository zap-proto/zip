package zip_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	if err := app.Add(zip.Load("/v1/demo", zip.Plugin{Name: "demo", Bin: bin})); err != nil {
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
	if err := app.Add(zip.Load("/v1/demo", zip.Plugin{Name: "demo", Bin: v1})); err != nil {
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
	if err := app.Add(zip.Load("/v1/demo", zip.Plugin{Name: "demo", Bin: v1})); err != nil {
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
	if err := app.Add(zip.Load("/v1/demo", zip.Plugin{Name: "demo", Bin: v1})); err != nil {
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
	if err := app.Add(zip.Load("/v1/demo", zip.Plugin{Name: "demo", Addr: sock})); err != nil {
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
	if err := app.Add(zip.Load("/v1/demo", zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Sum: hex.EncodeToString(sum[:]), Dir: cache,
	})); err != nil {
		t.Fatalf("Add(Load from release): %v", err)
	}
	defer func() { _ = app.Shutdown() }()

	if got := version(t, app); !strings.Contains(got, `"version":"v1"`) {
		t.Fatalf("body=%q, want the installed plugin's response", got)
	}

	// A second load at the same digest must reuse the cached binary rather than
	// download again — that is what makes a restart offline.
	app2 := zip.New(zip.Config{AppName: "host2", DisableStartupMessage: true})
	if err := app2.Add(zip.Load("/v1/demo", zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Sum: hex.EncodeToString(sum[:]), Dir: cache,
	})); err != nil {
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
	err := app.Add(zip.Load("/v1/demo", zip.Plugin{
		Name: "demo", URL: srv.URL + "/demo", Dir: t.TempDir(),
		Sum: strings.Repeat("00", 32), // wrong on purpose
	}))
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
	err := app.Add(zip.Load("/v1/demo", zip.Plugin{Name: "demo", URL: "https://example.test/x"}))
	if err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("err = %v, want a refusal naming the unverified download", err)
	}
}
