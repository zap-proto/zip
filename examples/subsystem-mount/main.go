// subsystem-mount example — HIP-0106 Mount(app, deps) idiom.
//
// One zip.App is composed from N subsystems via the Mount(...) contract.
// Each subsystem is a Go package that exposes:
//
//	func Mount(app *zip.App, deps Deps) error
//
// matching the gin-side hanzoai/commerce/checkout.MountPublic pattern.
package main

import (
	"context"
	"log"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// Deps is the typed dependency bag a unified Hanzo binary builds once
// and threads through every Mount(). Real deployments inject IAM, KMS,
// DocDB, ClickHouse, etc. — this example uses an opaque struct.
type Deps struct {
	OrgScoped func(ctx context.Context, org string) any
}

// usersSubsystem mounts user routes at /v1/users.
type usersSubsystem struct{}

func (usersSubsystem) Mount(app *zip.App, _ Deps) error {
	users := app.Group("/v1/users")
	users.Get("/:id", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{
			"id":  c.Param("id"),
			"org": c.Org(),
		})
	})
	return nil
}

// healthSubsystem mounts health probes.
type healthSubsystem struct{}

func (healthSubsystem) Mount(app *zip.App, _ Deps) error {
	app.Get("/healthz", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})
	return nil
}

func main() {
	app := zip.New(zip.Config{AppName: "subsystem-mount"})
	app.Use(middleware.Recover(), middleware.RequestID())

	deps := Deps{}
	if err := (healthSubsystem{}).Mount(app, deps); err != nil {
		log.Fatal(err)
	}
	if err := (usersSubsystem{}).Mount(app, deps); err != nil {
		log.Fatal(err)
	}

	log.Fatal(app.ListenHTTP(":8080"))
}
