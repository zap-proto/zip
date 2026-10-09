// Package ws is the one WebSocket upgrade zip performs. wsx.Upgrade (an untyped
// route) and a typed op answering zip.Socket both reach it, so a connection
// is accepted, refused and configured the same way whichever door it came in.
package ws

import (
	"errors"
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
	// Nil admits a request with no Origin, or one whose Origin is the
	// request's own host: a browser on another site is refused.
	CheckOrigin func(ctx *fasthttp.RequestCtx) bool
}

// Is reports whether the request asks to become a WebSocket.
func Is(rc *fasthttp.RequestCtx) bool { return websocket.FastHTTPIsWebSocketUpgrade(rc) }

// ErrOrigin is what Upgrade answers when the request's Origin may not open a
// connection, before anything is written, so the caller refuses it with a
// status of its own.
var ErrOrigin = errors.New("this origin may not open a connection here")

// Upgrade answers 101 and runs fn on the connection once the handler that
// called it returns. The caller has already checked [Is].
func Upgrade(rc *fasthttp.RequestCtx, cfg Config, fn func(*Conn)) error {
	check := cfg.CheckOrigin
	if check == nil {
		check = sameOrigin
	}
	if !check(rc) {
		return ErrOrigin
	}
	up := &websocket.FastHTTPUpgrader{
		ReadBufferSize:    cfg.ReadBufferSize,
		WriteBufferSize:   cfg.WriteBufferSize,
		Subprotocols:      cfg.Subprotocols,
		EnableCompression: cfg.EnableCompression,
		CheckOrigin:       func(*fasthttp.RequestCtx) bool { return true }, // asked above
	}
	return up.Upgrade(rc, fn)
}

// sameOrigin admits a request with no Origin — not a browser — and one whose
// Origin names the host the request was sent to.
func sameOrigin(rc *fasthttp.RequestCtx) bool {
	origin := rc.Request.Header.Peek("Origin")
	if len(origin) == 0 {
		return true
	}
	u, err := url.Parse(string(origin))
	return err == nil && strings.EqualFold(u.Host, string(rc.Host()))
}

// Refusal is the sentence a plain request to a WebSocket address is answered
// with, beside 426 and Upgrade: websocket (RFC 9110 §15.5.22).
const Refusal = "this address speaks WebSocket: connect with an Upgrade: websocket request"
