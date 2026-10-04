// Command testplugin is a minimal zip plugin used by the reload tests. It
// serves one route reporting the version stamped in at link time, so a test can
// prove WHICH build answered — the only honest way to show a reload swapped
// processes rather than just restarting the same one.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zap-proto/zip"
)

// version is set with -ldflags "-X main.version=…" so two builds of identical
// source are distinguishable on the wire.
var version = "unset"

// Version reports which build is answering.
type Version struct {
	Version string `json:"version"`
}

// Crashing acknowledges a crash request before the process dies.
type Crashing struct {
	Crashing string `json:"crashing"`
}

// Nothing is an operation that takes no input.
type Nothing struct{}

// tenant is a per-caller tool source: the half of a door that cannot be
// projected, because the answer depends on WHO is asking. A host that declares
// this plugin Open asks it per caller, and this is what answers.
type tenant struct{}

func (tenant) Tools(ctx context.Context) []map[string]any {
	org := orgOf(ctx)
	if org == "" {
		return nil
	}
	return []map[string]any{{
		"name": org + "_own", "description": "a tool only " + org + " has",
		"inputSchema": map[string]any{"type": "object"},
	}}
}

func (tenant) Call(ctx context.Context, name string, _ json.RawMessage) (any, error) {
	if org := orgOf(ctx); org != "" && name == org+"_own" {
		return map[string]any{"ran": name, "org": org}, nil
	}
	return nil, zip.Errorf(404, "unknown tool: %s", name)
}

// orgKey carries the caller the door is serving, parked by the middleware below
// the way a host's identity boundary does.
type orgKey struct{}

func orgOf(ctx context.Context) string { org, _ := ctx.Value(orgKey{}).(string); return org }

func main() {
	app := zip.New(zip.Config{AppName: "testplugin", DisableStartupMessage: true,
		MCP: zip.MCPConfig{Source: tenant{}}})
	app.Use(zip.H(func(c *zip.Ctx) error {
		c.SetContext(context.WithValue(c.Context(), orgKey{}, c.Org()))
		return c.Continue()
	}))

	// Typed, like any other route worth having: a plugin is an ordinary zip
	// app, so its ops project into the host's document, tools and call plane
	// exactly as a linked-in service's do.
	app.Get("/v1/demo/version", func(context.Context, *Nothing) (*Version, error) {
		return &Version{Version: version}, nil
	}, zip.WithSummary("Reports which build of the plugin is answering."))

	// Crashes the process the way a real bug does — a panic on a goroutine,
	// which no handler recover() can catch. Used to prove the host survives a
	// plugin dying and brings it back.
	app.Get("/v1/demo/crash", func(context.Context, *Nothing) (*Crashing, error) {
		go func() { panic("testplugin: deliberate crash") }()
		return &Crashing{Crashing: "true"}, nil
	}, zip.WithSummary("Crashes the plugin process, to prove the host survives it."))

	// Untyped on purpose, and the one route here that has to be. It echoes
	// whatever path reached it, so a host that mounts this plugin at more than
	// one prefix can tell "the second prefix is not mounted" (the HOST 404s)
	// from "the plugin has no such route" (the PLUGIN 404s) — which are
	// otherwise identical from the outside. A wildcard answers paths that do not
	// exist, so there is no operation for it to be: an op is one named endpoint
	// with one schema, and this is deliberately neither.
	//
	// ?sleep=<duration> answers after that long and ?drip=<duration> streams a
	// line every 50ms for that long, then "done". They make a request that is
	// still in flight when something else happens — what the eviction tests need,
	// at any prefix the plugin is mounted under.
	app.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error {
		if d, err := time.ParseDuration(c.Query("drip")); err == nil {
			return c.SendStreamWriter(func(w *bufio.Writer) {
				for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
					_, _ = w.WriteString("tick\n")
					if w.Flush() != nil {
						return
					}
				}
				_, _ = w.WriteString("done\n")
				_ = w.Flush()
			})
		}
		if d, err := time.ParseDuration(c.Query("sleep")); err == nil {
			time.Sleep(d)
		}
		return c.JSON(200, map[string]string{"echo": c.Path(), "version": version})
	})

	// `testplugin tools` prints this plugin's MCP catalogue instead of serving —
	// the build-time step a host's Plugin.Tools is captured from, and the same
	// thing hanzoai/cloud's `<app> describe` does. Projecting it is the whole of
	// what a plugin does to be on a host's composed MCP door.
	if len(os.Args) > 1 && os.Args[1] == "tools" {
		b, err := json.Marshal(app.MCPTools())
		if err != nil {
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(b)
		return
	}

	// TESTPLUGIN_ON_TERM names a file the plugin writes when SIGTERM arrives,
	// TESTPLUGIN_TERM_DELAY after it, then exits — standing in for the stores a
	// real child closes and ships on the way out. TESTPLUGIN_TERM=ignore makes
	// it ignore SIGTERM, so only a kill ends it.
	switch {
	case os.Getenv("TESTPLUGIN_TERM") == "ignore":
		signal.Ignore(syscall.SIGTERM)
	case os.Getenv("TESTPLUGIN_ON_TERM") != "":
		go onTerm(os.Getenv("TESTPLUGIN_ON_TERM"))
	}

	// Addr is the whole plugin side of the contract: serve where the host said.
	// Report rather than swallow: a fixture that discards this error exits
	// silently and the host reports only "exited before listening".
	if err := app.Listen(zip.Addr(":9999")); err != nil {
		log.Fatalf("testplugin: %v", err)
	}
}

// onTerm waits for SIGTERM, takes TESTPLUGIN_TERM_DELAY, writes path and exits.
func onTerm(path string) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	d, _ := time.ParseDuration(os.Getenv("TESTPLUGIN_TERM_DELAY"))
	time.Sleep(d)
	if err := os.WriteFile(path, []byte("drained"), 0o600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
