// The host. It composes a linked-in service and a plugin with the same verb,
// and never imports the plugin's code.
package main

import (
	"context"
	"log"

	"github.com/zap-proto/zip"
)

// Health is what the probe answers.
type Health struct {
	Status string `json:"status"`
}

// Nothing is an operation that takes no input.
type Nothing struct{}

// health is a linked-in unit: a constructor that RETURNS a definition. A unit
// that takes dependencies returns the same thing — func(deps) *zip.App — so a
// composition root only ever sees *App, whether the unit is linked in or lives
// in its own binary.
//
// It registers a typed op, exactly as the separately-built plugin does. Both
// halves of the composition contribute to one document, one tool list and one
// command tree, which is what makes "linked in" versus "its own binary" a
// deployment decision and nothing more.
func health() *zip.App {
	a := zip.New(zip.Config{AppName: "health", DisableStartupMessage: true})
	zip.Get(a, "/health", func(context.Context, *Nothing) (*Health, error) {
		return &Health{Status: "ok"}, nil
	})
	return a
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
	// Or install the one CI published, which is what decouples the two build
	// cycles entirely — the plugin is built ONCE, per OS/arch, and every host
	// picks up the same bits. Sum is required: the digest is verified before the
	// file is ever made executable, and it doubles as the cache key, so a
	// restart is offline and a rollback is free. See ../README.md.
	//
	//	zip.Load(zip.Plugin{
	//	    Name: "billing",
	//	    URL:  "https://github.com/hanzoai/billing/releases/download/v1.2.3/billing-linux-arm64",
	//	    Sum:  "9f2c…",
	//	    Dir:  "/var/lib/hanzo", // not /tmp — that is RAM on most hosts
	//	}, "/v1/billing")
	//
	// Or point at one that is already running elsewhere, unchanged otherwise:
	//
	//	zip.Load(zip.Plugin{Name: "billing", Addr: os.Getenv("BILLING_ADDR")}, "/v1/billing")
	billing, err := zip.Load(zip.Plugin{Name: "billing", Path: "./bin/billing"}, "/v1/billing")
	if err != nil {
		log.Fatal(err)
	}
	// ONE verb. Middleware, a linked-in unit and a plugin in its own binary all
	// arrive the same way, because they are the same kind of thing: something
	// that answers addresses.
	app.Use(health(), billing)

	// A new build of the plugin can replace the running one without dropping a
	// request — the replacement must be listening before any traffic moves to
	// it, so a bad build cannot take the route down:
	//
	//	app.Reload("billing", newBin)
	//
	// The child is killed and cleaned up on Shutdown.
	log.Fatal(app.Listen(":9653", "http://:8080"))
}
