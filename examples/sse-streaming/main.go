// sse-streaming example — Server-Sent Events via c.SendStreamWriter.
// LLM-gateway pattern: stream tokens as they arrive from upstream.
//
//	go run ./examples/sse-streaming
//	curl -N http://localhost:8080/v1/stream
//
// # Untyped on purpose — one of the few places it is right
//
// A typed op is (In → Out): zip decodes the input, runs the handler ONCE, and
// serializes the single value it returns. This route has no single value to
// return. It holds the connection open and writes frames as they arrive, which
// is what SSE IS, so there is nothing for a response schema to describe and
// nothing an MCP tools/call could hand back.
//
// The cost is real and worth stating: this route is in no OpenAPI document, is
// no MCP tool, and no service can reach it with zip.Call. Everything that CAN
// name its output should be a typed op — see examples/hello. Reach for an
// untyped handler when the response is a stream, an upgrade, a proxied byte
// range or a non-JSON body, and for nothing else.
package main

import (
	"bufio"
	"fmt"
	"log"
	"time"

	"github.com/zap-proto/zip"
)

func main() {
	app := zip.New(zip.Config{AppName: "sse-streaming"})

	app.Get("/v1/stream", func(c *zip.Ctx) error {
		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")

		return c.SendStreamWriter(func(w *bufio.Writer) {
			for i := 0; i < 10; i++ {
				_, _ = fmt.Fprintf(w, "data: tick %d\n\n", i)
				if err := w.Flush(); err != nil {
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
		})
	})

	log.Fatal(app.Listen("http://:8080"))
}
