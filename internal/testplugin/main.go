// Command testplugin is a minimal zip plugin used by the reload tests. It
// serves one route reporting the version stamped in at link time, so a test can
// prove WHICH build answered — the only honest way to show a reload swapped
// processes rather than just restarting the same one.
package main

import (
	"context"

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

func main() {
	app := zip.New(zip.Config{AppName: "testplugin", DisableStartupMessage: true})

	// Typed, like any other route worth having: a plugin is an ordinary zip
	// app, so its ops project into the host's document, tools and call plane
	// exactly as a linked-in service's do.
	zip.Get(app, "/v1/demo/version", func(context.Context, *Nothing) (*Version, error) {
		return &Version{Version: version}, nil
	})

	// Crashes the process the way a real bug does — a panic on a goroutine,
	// which no handler recover() can catch. Used to prove the host survives a
	// plugin dying and brings it back.
	zip.Get(app, "/v1/demo/crash", func(context.Context, *Nothing) (*Crashing, error) {
		go func() { panic("testplugin: deliberate crash") }()
		return &Crashing{Crashing: "true"}, nil
	})

	// Untyped on purpose, and the one route here that has to be. It echoes
	// whatever path reached it, so a host that mounts this plugin at more than
	// one prefix can tell "the second prefix is not mounted" (the HOST 404s)
	// from "the plugin has no such route" (the PLUGIN 404s) — which are
	// otherwise identical from the outside. A wildcard answers paths that do not
	// exist, so there is no operation for it to be: an op is one named endpoint
	// with one schema, and this is deliberately neither.
	app.All("/*", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"echo": c.Path(), "version": version})
	})

	// Addr is the whole plugin side of the contract: serve where the host said.
	_ = app.Listen(zip.Addr(":9999"))
}
