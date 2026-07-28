// migrate-from-gin example — mechanical port of a gin-style API to zip.
//
// gin before:
//
//	r := gin.Default()
//	r.GET("/users/:id", func(c *gin.Context) {
//	    c.JSON(200, gin.H{"id": c.Param("id")})
//	})
//
// zip after — Sinatra/Express idiom is preserved; gin.Context becomes
// *zip.Ctx; gin.H becomes any map-shaped value.
//
// # The port is two steps, and stopping after the first loses the point
//
// STEP 1 is mechanical and shown below: the handler shape is the same, so a
// route moves in one edit. It is a WAY-STATION. An untyped route registers no
// operation, so after step 1 the endpoint is in no OpenAPI document, is no MCP
// tool, has no command, and no service can reach it with zip.Call — which is
// exactly the surface gin gave you, and the surface you are migrating away from.
//
// STEP 2 declares the In/Out types the handler was already parsing by hand and
// registers the route as a typed op. That is where the projections turn on. Port
// mechanically to get it running, then type it; do not ship a service that
// stopped halfway.
//
// A typed op takes the whole path — zip.Get(app, …) registers on the App, so
// there is no Group prefix to inherit. A gin codebase built on router groups
// spells the prefix out once per route on the way across.
package main

import (
	"context"
	"log"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// GetUserIn addresses one user; `id` is the path segment.
type GetUserIn struct {
	ID string `json:"id"`
}

// User is one user record.
type User struct {
	ID string `json:"id"`
}

// CreateUserIn is the create body — the same field the untyped port bound by
// hand with c.Bind, declared once instead.
type CreateUserIn struct {
	Name string `json:"name" validate:"required"`
}

func main() {
	app := zip.New(zip.Config{AppName: "migrate-from-gin"})
	app.Use(middleware.Recover(), middleware.RequestID(), middleware.Logger(app.Logger()))

	// STEP 1 — the mechanical port. The same shape as the gin handler, running
	// on zip after one edit, and invisible to every projection until step 2.
	app.Get("/legacy/users/:id", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"id": c.Param("id")})
	})

	// STEP 2 — the same routes as typed ops. The hand-written c.Param and
	// c.Bind are gone: the In type says what the request IS, and one declaration
	// feeds the route, the document, the tool list, the CLI and the call plane.
	zip.Get(app, "/v1/users/:id", getUser)
	zip.Post(app, "/v1/users", createUser)

	log.Fatal(app.Listen("http://:8080"))
}

// GetUser returns one user by id.
func getUser(_ context.Context, in *GetUserIn) (*User, error) {
	return &User{ID: in.ID}, nil
}

// CreateUser creates a user. A missing name is refused before the handler runs,
// by the same `validate:"required"` tag that makes the field required in the
// published schema.
func createUser(_ context.Context, in *CreateUserIn) (*CreateUserIn, error) {
	return in, nil
}
