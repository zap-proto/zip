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
//
// A typed op that answers a WebSocket returns a [zip.Socket] instead; both run
// the same upgrade (internal/ws), so a connection is accepted and refused the
// same way at either kind of route.
package wsx

import (
	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/internal/ws"
)

// Conn is the WebSocket connection passed to wsx handlers.
type Conn = ws.Conn

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

// Config configures the WebSocket upgrade. It is the type [zip.Socket]'s
// Config field holds.
type Config = ws.Config

// Upgrade returns a zip.Handler that upgrades the HTTP connection to a
// WebSocket and calls fn with the established *Conn.
func Upgrade(fn Handler, opts ...Config) zip.Handler {
	var cfg Config
	if len(opts) > 0 {
		cfg = opts[0]
	}
	// TERMINAL: the upgrade takes over the connection, so this answers the
	// address it is registered at and never yields to anything after it.
	// zip.Terminal is what makes app.Use(wsx.Upgrade(…)) a build error instead
	// of a server that tries to upgrade every request it receives.
	return zip.Terminal("wsx.Upgrade", func(c *zip.Ctx) error {
		rc := c.Fiber().RequestCtx()
		// A plain request to a WebSocket address is the client's mistake — a
		// crawler, a health probe, a browser tab opened on the URL. Answered 426
		// (RFC 9110 §15.5.22) with the protocol to switch to, as a client error,
		// rather than handed to the upgrader, whose handshake error is reported
		// as a server fault.
		if !ws.Is(rc) {
			c.SetHeader("Upgrade", "websocket")
			return zip.Errorf(fasthttp.StatusUpgradeRequired, ws.Refusal)
		}
		return ws.Upgrade(rc, cfg, func(conn *Conn) { _ = fn(conn) })
	})
}
