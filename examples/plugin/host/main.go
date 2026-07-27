// The host. It composes a linked-in service and a plugin with the same verb,
// and never imports the plugin's code.
package main

import (
	"log"

	"github.com/zap-proto/zip"
)

// health is a linked-in Service: a plain func(*zip.App) error. A constructor
// that takes dependencies and returns one of these is the same thing curried,
// which is why a composition root only ever sees Service.
func health(a *zip.App) error {
	a.Get("/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})
	return nil
}

func main() {
	app := zip.New(zip.Config{AppName: "host"})

	// Both arguments are Services. The first is linked into this binary; the
	// second is a separate binary started on its own unix socket and reached
	// over ZAP. Moving a service between those two lines is a deployment
	// decision, not a code change.
	//
	// Path is used here so the example runs from a fresh checkout. In a real
	// deployment you embed the binary instead and still ship one artifact:
	//
	//	//go:embed bin/billing
	//	var billingBin []byte
	//	zip.Load(zip.Plugin{Name: "billing", Bin: billingBin}, "/v1/billing")
	//
	// Or point at one that is already running elsewhere, unchanged otherwise:
	//
	//	zip.Load(zip.Plugin{Name: "billing", Addr: os.Getenv("BILLING_ADDR")}, "/v1/billing")
	if err := app.Add(
		health,
		zip.Load(zip.Plugin{Name: "billing", Path: "./bin/billing"}, "/v1/billing"),
	); err != nil {
		log.Fatal(err)
	}

	// A new build of the plugin can replace the running one without dropping a
	// request — the replacement must be listening before any traffic moves to
	// it, so a bad build cannot take the route down:
	//
	//	app.Reload("billing", newBin)
	//
	// The child is killed and cleaned up on Shutdown.
	log.Fatal(app.Listen(":9653", "http://:8080"))
}
