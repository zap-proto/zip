// Package ws is the one WebSocket upgrade zip performs. wsx.Upgrade (an untyped
// route) and a typed op answering zip.Socket both reach it.
package ws

import (
	"net/url"
	"strings"

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
	// Nil is the library's: an Origin, when there is one, must name the
	// request's own host.
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
	return up.Upgrade(rc, fn)
}

// Version is the protocol version a handshake must ask for (RFC 6455).
const Version = "13"

// Speaks reports whether a handshake's Sec-WebSocket-Version list names
// [Version], read as the library reads it: comma-separated, case aside.
func Speaks(rc *fasthttp.RequestCtx) bool {
	for _, v := range strings.Split(string(rc.Request.Header.Peek("Sec-WebSocket-Version")), ",") {
		if strings.EqualFold(strings.TrimSpace(v), Version) {
			return true
		}
	}
	return false
}

// Admits reports whether a request's Origin may open a connection: a request
// with no Origin (not a browser), one whose Origin names the request's own host,
// and one whose Origin is among origins, "*" being any.
func Admits(rc *fasthttp.RequestCtx, origins []string) bool {
	origin := string(rc.Request.Header.Peek("Origin"))
	if origin == "" {
		return true
	}
	for _, o := range origins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, string(rc.Host()))
}

// Refusal is the sentence a plain request to a WebSocket address is answered
// with, beside 426 and Upgrade: websocket (RFC 9110 §15.5.22).
const Refusal = "this address speaks WebSocket: connect with an Upgrade: websocket request"
