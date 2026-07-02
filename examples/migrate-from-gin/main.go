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
// *zip.Ctx; gin.H becomes any map-shaped value (or a typed struct).
package main

import (
	"log"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

func main() {
	app := zip.New(zip.Config{AppName: "migrate-from-gin"})
	app.Use(middleware.Recover(), middleware.RequestID(), middleware.Logger(app.Logger()))

	app.Get("/users/:id", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{
			"id": c.Param("id"),
		})
	})

	v1 := app.Group("/v1")
	v1.Post("/users", func(c *zip.Ctx) error {
		var body struct {
			Name string `json:"name" validate:"required"`
		}
		if err := c.Bind(&body); err != nil {
			// c.Bind already returns *zip.HTTPError(400) on failure.
			return err
		}
		return c.JSON(201, body)
	})

	log.Fatal(app.Listen("http://:8080"))
}
