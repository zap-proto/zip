// Package wsx provides Fiber-v3-compatible WebSocket support via
// fasthttp/websocket. The public surface is intentionally small:
//
//	app.Get("/ws", wsx.Upgrade(func(c *wsx.Conn) error {
//	    for {
//	        _, msg, err := c.ReadMessage()
//	        if err != nil { return err }
//	        _ = c.WriteMessage(wsx.TextMessage, msg)
//	    }
//	}))
package wsx

import (
	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"

	"github.com/zap-proto/zip"
)

// Conn is the WebSocket connection passed to wsx handlers.
type Conn = websocket.Conn

// Message-type constants re-exported for convenience.
const (
	TextMessage   = websocket.TextMessage
	BinaryMessage = websocket.BinaryMessage
	CloseMessage  = websocket.CloseMessage
	PingMessage   = websocket.PingMessage
	PongMessage   = websocket.PongMessage
)

// Handler is the wsx handler signature.
type Handler func(c *Conn) error

// Config configures the WebSocket upgrade.
type Config struct {
	ReadBufferSize    int
	WriteBufferSize   int
	Subprotocols      []string
	EnableCompression bool
	// CheckOrigin defaults to allow-all (zip is multi-tenant; gate via
	// CORS or auth middleware at the route level instead).
	CheckOrigin func(ctx *fasthttp.RequestCtx) bool
}

// Upgrade returns a zip.Handler that upgrades the HTTP connection to a
// WebSocket and calls fn with the established *Conn.
func Upgrade(fn Handler, opts ...Config) zip.Handler {
	var cfg Config
	if len(opts) > 0 {
		cfg = opts[0]
	}
	up := &websocket.FastHTTPUpgrader{
		ReadBufferSize:    cfg.ReadBufferSize,
		WriteBufferSize:   cfg.WriteBufferSize,
		Subprotocols:      cfg.Subprotocols,
		EnableCompression: cfg.EnableCompression,
	}
	if cfg.CheckOrigin != nil {
		up.CheckOrigin = cfg.CheckOrigin
	} else {
		up.CheckOrigin = func(ctx *fasthttp.RequestCtx) bool { return true }
	}
	return func(c *zip.Ctx) error {
		rc := c.Fiber().RequestCtx()
		return up.Upgrade(rc, func(ws *Conn) {
			_ = fn(ws)
		})
	}
}
