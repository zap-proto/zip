// The plugin. An ordinary zip app — no SDK, no schema, nothing plugin-specific
// except where it listens. Build it, and a host can load it.
package main

import (
	"log"

	"github.com/zap-proto/zip"
)

func main() {
	app := zip.New(zip.Config{AppName: "billing"})

	app.Get("/v1/billing/invoices", func(c *zip.Ctx) error {
		return c.JSON(200, []map[string]any{
			{"id": "inv_1", "cents": 1200},
			{"id": "inv_2", "cents": 3400},
		})
	})

	app.Post("/v1/billing/charge", func(c *zip.Ctx) error {
		return c.JSON(201, map[string]string{"status": "charged"})
	})

	// zip.Addr returns the socket a host asked this process to serve on, and
	// falls back to the argument when it is run directly. That fallback is the
	// whole reason the same binary works standalone and as a plugin.
	log.Fatal(app.Listen(zip.Addr(":9654")))
}
