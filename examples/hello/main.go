// Hello, zip — minimal example.
//
//	go run ./examples/hello
//	curl http://localhost:8080/hello
package main

import (
	"log"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

func main() {
	app := zip.New(zip.Config{AppName: "hello"})
	app.Use(middleware.Recover(), middleware.RequestID())

	app.Get("/hello", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"message": "hello world"})
	})
	app.Get("/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})

	log.Fatal(app.Listen("http://:8080"))
}
