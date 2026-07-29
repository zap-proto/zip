// subsystem-mount example — HIP-0106 Mount(app, deps) idiom.
//
// One zip.App is composed from N subsystems via the Mount(...) contract.
// Each subsystem is a Go package that exposes:
//
//	func Mount(app *zip.App, deps Deps) error
//
// matching the gin-side hanzoai/commerce/checkout.MountPublic pattern.
//
// Each subsystem declares TYPED ops, which is what makes composition compose the
// whole surface: the assembled binary's OpenAPI document, MCP tool list and
// command line are the union of what its subsystems registered, with nothing
// written twice and no subsystem knowing the others exist. A subsystem that
// registered untyped routes would contribute traffic and nothing else.
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

// GetUserIn addresses one user. `id` is the path segment: the URL is the
// addressing authority, so it binds from there whatever else the request says.
type GetUserIn struct {
	ID string `json:"id"`
}

// User is one user record.
type User struct {
	ID  string `json:"id"`
	Org string `json:"org"`
}

// Health is what the probe answers.
type Health struct {
	Status string `json:"status"`
}

// Nothing is an operation that takes no input.
type Nothing struct{}

// usersSubsystem mounts user routes at /v1/users.
type usersSubsystem struct{}

func (usersSubsystem) Mount(app *zip.App, _ Deps) error {
	// The subsystem owns a prefix, so it takes a Group and declares its ops on
	// that — the prefix is part of every op it registers, and the composition
	// root never repeats it.
	users := app.Group("/v1/users")

	// GetUser returns one user, scoped to the caller's org.
	zip.Get(users, "/:id", func(ctx context.Context, in *GetUserIn) (*User, error) {
		// The gateway's identity reaches a typed handler through the ctx —
		// c.Org() is the untyped spelling of the same headers.
		return &User{ID: in.ID, Org: zip.CallerOf(ctx).Org}, nil
	})
	return nil
}

// healthSubsystem mounts health probes.
type healthSubsystem struct{}

func (healthSubsystem) Mount(app *zip.App, _ Deps) error {
	zip.Get(app, "/healthz", func(context.Context, *Nothing) (*Health, error) {
		return &Health{Status: "ok"}, nil
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

	log.Fatal(app.Listen("http://:8080"))
}
