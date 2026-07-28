// websocket example — chat-style echo server.
//
//	go run ./examples/websocket
//	wscat -c ws://localhost:8080/ws
//
// # Untyped on purpose — the request never completes
//
// An upgrade hands the connection to another protocol: there is no response
// body for a typed op's Out to be, and the exchange that follows is frames in
// both directions for as long as the peer stays. A typed op is one decode, one
// call, one serialize, so it cannot express this — which is why wsx.Upgrade
// takes an untyped handler and not a TypedHandler.
//
// The cost is the same one every untyped route pays: no OpenAPI entry, no MCP
// tool, not reachable by zip.Call. Anything with a request-and-response shape
// belongs in a typed op instead — see examples/hello.
package main

import (
	"log"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
	"github.com/zap-proto/zip/wsx"
)

func main() {
	app := zip.New(zip.Config{AppName: "websocket"})
	app.Use(middleware.Recover(), middleware.RequestID())

	app.Get("/ws", wsx.Upgrade(func(c *wsx.Conn) error {
		log.Printf("ws connection from %s", c.RemoteAddr())
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				log.Printf("ws read: %v", err)
				return err
			}
			if err := c.WriteMessage(mt, msg); err != nil {
				return err
			}
		}
	}))

	log.Fatal(app.Listen("http://:8080"))
}
