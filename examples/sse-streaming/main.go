// sse-streaming example — Server-Sent Events via c.SendStreamWriter.
// LLM-gateway pattern: stream tokens as they arrive from upstream.
//
//	go run ./examples/sse-streaming
//	curl -N http://localhost:8080/v1/stream
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

	log.Fatal(app.ListenHTTP(":8080"))
}
