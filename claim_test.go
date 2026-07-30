package zip_test

import (
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The defect this closes: a SECOND claim of a prefix used to return nil and the
// first registration kept answering. The losing plugin's whole surface then
// served another plugin's 404, and nothing in the composition said so — which is
// why a host's conflict gate has to run at build time.
func TestASecondClaimOfAPrefixIsRefused(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{Name: "a", Addr: "/run/zip/a.sock"}, "/v1/x")); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	err := app.Add(zip.Load(zip.Plugin{Name: "b", Addr: "/run/zip/b.sock"}, "/v1/x"))
	if err == nil {
		t.Fatal("the second claim of /v1/x was accepted — the losing plugin serves a's 404")
	}
	for _, want := range []string{"/v1/x", `"a"`, `"b"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %s", err, want)
		}
	}
	// "/v1/x" and "/v1/x/" are ONE claim: the router normalises, so the gate must.
	if err := app.Add(zip.Load(zip.Plugin{Name: "c", Addr: "/run/zip/c.sock"}, "/v1/x/")); err == nil {
		t.Fatal("a trailing slash bought a second claim on the same subtree")
	}
}

// All-or-nothing: a plugin owning three prefixes and losing the third must not
// leave the first two held, or the plugins that really own them cannot load.
func TestAPartialClaimIsRolledBack(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{Name: "held", Addr: "/run/zip/h.sock"}, "/v1/c")); err != nil {
		t.Fatal(err)
	}
	if err := app.Add(zip.Load(zip.Plugin{Name: "wide", Addr: "/run/zip/w.sock"},
		"/v1/a", "/v1/b", "/v1/c")); err == nil {
		t.Fatal("wide took /v1/c from held")
	}
	// /v1/a and /v1/b must be free for whoever really owns them.
	for _, p := range []string{"/v1/a", "/v1/b"} {
		if err := app.Add(zip.Load(zip.Plugin{Name: "real" + p, Addr: "/run/zip/r.sock"}, p)); err != nil {
			t.Fatalf("%s stayed held by a plugin that failed to load: %v", p, err)
		}
	}
}

// A bare Mount is the same claim through a different door, so it is refused the
// same way — otherwise the gate is one that any caller can walk around.
func TestMountAndLoadShareOneClaimRegister(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Mount("/v1/y", "/run/zip/y.sock"); err != nil {
		t.Fatal(err)
	}
	if err := app.Add(zip.Load(zip.Plugin{Name: "z", Addr: "/run/zip/z.sock"}, "/v1/y")); err == nil {
		t.Fatal("Load walked around a claim Mount already held")
	}
	if err := app.Mount("/v1/y", "/run/zip/other.sock"); err == nil {
		t.Fatal("Mount walked around its own claim")
	}
}
