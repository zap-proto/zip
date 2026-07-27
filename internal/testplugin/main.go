// Command testplugin is a minimal zip plugin used by the reload tests. It
// serves one route reporting the version stamped in at link time, so a test can
// prove WHICH build answered — the only honest way to show a reload swapped
// processes rather than just restarting the same one.
package main

import "github.com/zap-proto/zip"

// version is set with -ldflags "-X main.version=…" so two builds of identical
// source are distinguishable on the wire.
var version = "unset"

func main() {
	app := zip.New(zip.Config{AppName: "testplugin", DisableStartupMessage: true})
	app.Get("/v1/demo/version", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"version": version})
	})
	// Addr is the whole plugin side of the contract: serve where the host said.
	_ = app.Listen(zip.Addr(":9999"))
}
