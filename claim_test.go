package zip_test

import (
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// Two definitions cannot own one address. That rule used to live in a private
// prefix-claim ledger (App.claim/release) that only Mount and Load consulted;
// it now lives where every OTHER address collision is already decided — the
// walk, at build. One mechanism, so there is no second answer to disagree with
// the first, and the report is strictly better: every collision at once, with
// both claimants and their call sites named.
//
// The defect the old ledger closed is still closed, and these prove it.

func plugin(t *testing.T, name, prefix string) *zip.App {
	t.Helper()
	p, err := zip.Load(zip.Plugin{Name: name, Addr: "/run/zip/" + name + ".sock"}, prefix)
	if err != nil {
		t.Fatalf("Load(%s): %v", name, err)
	}
	return p
}

// A second definition claiming a held prefix is refused. Before, the second
// claim returned nil and the first registration kept answering, so the losing
// plugin's whole surface served the winner's 404 and nothing said so.
func TestASecondClaimOfAPrefixIsRefused(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(plugin(t, "a", "/v1/x"), plugin(t, "b", "/v1/x"))

	err := app.Build()
	if err == nil {
		t.Fatal("two definitions claiming /v1/x were both accepted")
	}
	for _, want := range []string{"/v1/x", `"a"`, `"b"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %s: %v", want, err)
		}
	}
}

// A refused composition changes nothing: the live generation is whatever was
// valid, and a half-applied plugin is not a state that exists.
func TestARefusedCompositionIsRolledBack(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(plugin(t, "first", "/v1/a"))
	if err := app.Build(); err != nil {
		t.Fatalf("first build: %v", err)
	}

	// A second plugin that collides with the first.
	if err := app.Include(plugin(t, "second", "/v1/a")); err == nil {
		t.Fatal("a colliding plugin was included")
	}
	if n, _ := app.Generation(); n != 0 {
		t.Errorf("a refused Include advanced the generation to %d", n)
	}
	// The plugin table still holds exactly the one that was valid.
	names := []string{}
	for _, p := range app.Plugins() {
		names = append(names, p.Name)
	}
	if len(names) != 1 || names[0] != "first" {
		t.Errorf("plugins = %v, want [first]", names)
	}
}

// Mount and Load are the same kind of thing now — definition constructors — so
// they collide with each other exactly as two Loads do. There is no separate
// register for either.
func TestMountAndLoadCollideThroughOneMechanism(t *testing.T) {
	remote, err := zip.Mount("/v1/y", "/run/zip/y.sock")
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	app.Use(remote, plugin(t, "z", "/v1/y"))

	if err := app.Build(); err == nil {
		t.Fatal("a Mount and a Load claiming /v1/y were both accepted")
	} else if !strings.Contains(err.Error(), "/v1/y") {
		t.Errorf("refusal does not name the address: %v", err)
	}
}
