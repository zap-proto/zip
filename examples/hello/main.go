// Hello, zip — minimal example.
//
//	go run ./examples/hello
//	curl http://localhost:8080/hello
//	curl http://localhost:8080/.well-known/openapi.json   # the same two ops
//	curl -XPOST -d '{"method":"tools/list"}' http://localhost:8080/mcp
//
// Both routes are TYPED ops, which is the default. zip.Get[In, Out] registers
// ONE operation, and zip projects it into the REST route, the OpenAPI document,
// an MCP tool, a `<service> <operation>` command line and a by-name call plane.
// An untyped app.Get(path, func(c *zip.Ctx) error) registers no operation and
// therefore appears in NONE of them — see examples/sse-streaming for the case
// where that is still the right trade.
package main

import (
	"context"
	"log"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// Greeting is what GET /hello answers.
type Greeting struct {
	Message string `json:"message"`
}

// Health is what GET /health answers.
type Health struct {
	Status string `json:"status"`
}

// Empty is an operation that takes nothing. A GET binds its input from the URL,
// and these two read no part of it.
type Empty struct{}

func main() {
	app := zip.New(zip.Config{AppName: "hello"})
	app.Use(middleware.Recover(), middleware.RequestID())

	zip.Get(app, "/hello", hello)
	zip.Get(app, "/health", health)

	log.Fatal(app.Listen("http://:8080"))
}

// Hello greets the caller.
func hello(_ context.Context, _ *Empty) (*Greeting, error) {
	return &Greeting{Message: "hello world"}, nil
}

// Health reports whether the service is up.
func health(_ context.Context, _ *Empty) (*Health, error) {
	return &Health{Status: "ok"}, nil
}
