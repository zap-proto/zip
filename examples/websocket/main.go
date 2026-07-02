// websocket example — chat-style echo server.
//
//	go run ./examples/websocket
//	wscat -c ws://localhost:8080/ws
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
