// Package ws is the one WebSocket upgrade zip performs. wsx.Upgrade (an untyped
// route) and a typed op answering zip.Socket both reach it, so a connection
// is accepted, refused and configured the same way whichever door it came in.
package ws

import (
	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"
)

// Conn is an upgraded connection.
type Conn = websocket.Conn

// Config configures an upgrade.
type Config struct {
	// ReadBufferSize and WriteBufferSize size the connection's I/O buffers in
	// bytes; zero takes the library's default.
	ReadBufferSize  int
	WriteBufferSize int
	// Subprotocols are the protocols the server speaks, in its order of
	// preference; the first one the client also offers is selected.
	Subprotocols []string
	// EnableCompression negotiates per-message deflate when the client offers it.
	EnableCompression bool
	// CheckOrigin decides whether a request's Origin may open a connection.
	// Nil admits every origin: zip is multi-tenant and gates at the route.
	CheckOrigin func(ctx *fasthttp.RequestCtx) bool
}

// Is reports whether the request asks to become a WebSocket.
func Is(rc *fasthttp.RequestCtx) bool { return websocket.FastHTTPIsWebSocketUpgrade(rc) }

// Upgrade answers 101 and runs fn on the connection once the handler that
// called it returns. The caller has already checked [Is].
func Upgrade(rc *fasthttp.RequestCtx, cfg Config, fn func(*Conn)) error {
	up := &websocket.FastHTTPUpgrader{
		ReadBufferSize:    cfg.ReadBufferSize,
		WriteBufferSize:   cfg.WriteBufferSize,
		Subprotocols:      cfg.Subprotocols,
		EnableCompression: cfg.EnableCompression,
		CheckOrigin:       cfg.CheckOrigin,
	}
	if up.CheckOrigin == nil {
		up.CheckOrigin = func(*fasthttp.RequestCtx) bool { return true }
	}
	return up.Upgrade(rc, fn)
}

// Refusal is the sentence a plain request to a WebSocket address is answered
// with, beside 426 and Upgrade: websocket (RFC 9110 §15.5.22).
const Refusal = "this address speaks WebSocket: connect with an Upgrade: websocket request"
