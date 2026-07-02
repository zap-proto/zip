package zip

import (
	"fmt"
	"strings"
	"sync"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
)

// Transport is decomplected: ONE fiber handler, served on any number of
// addresses, where the transport is a VALUE (the address scheme) — never a
// method name. There is ONE verb, Listen, and the scheme selects how the
// bytes are terminated/serialized: ZAP, HTTP, or any future protocol you
// RegisterTransport. Same handler, middleware, auth, and error handling over
// every transport — your routes ARE the surface, no per-endpoint wiring.
//
//	app.Listen(":9653")                  // ZAP (the primary; bare addr = ZAP)
//	app.Listen(":9653", "http://:8080")  // ZAP + HTTP in one call
//	app.Listen("http://:8080")           // HTTP only
//	app.Listen("quic://:443")            // any RegisterTransport'd proto
//
// This mirrors net.Listen(network, addr): the network is a value, not a
// ListenTCP/ListenUDP method explosion.

// Server is a running transport listener bound to one address. Both
// zap-proto/http.Server and the built-in HTTP server satisfy it, as does any
// custom transport.
type Server interface {
	ListenAndServe() error
	Close() error
}

// TransportFunc builds a Server that serves handler on addr. Register one per
// address scheme with RegisterTransport — that is the ONLY extension point;
// the Listen API never changes as protocols are added.
type TransportFunc func(addr string, handler fasthttp.RequestHandler) Server

// DefaultScheme is the transport a bare address (no "scheme://") uses. ZAP is
// the primary transport (TLS 1.3 + post-quantum, gRPC's replacement), so the
// path of least resistance is ZAP-native.
const DefaultScheme = "zap"

var (
	transportsMu sync.RWMutex
	transports   = map[string]TransportFunc{
		"zap": func(addr string, h fasthttp.RequestHandler) Server {
			return &zaphttp.Server{Addr: addr, Handler: h}
		},
		"http": func(addr string, h fasthttp.RequestHandler) Server {
			return &httpServer{addr: addr, srv: &fasthttp.Server{Handler: h}}
		},
	}
)

// RegisterTransport adds (or replaces) a transport keyed by address scheme, so
// any future termination/serialization protocol slots in with ZERO change to
// the Listen API. Call before Listen.
//
//	zip.RegisterTransport("quic", func(addr string, h fasthttp.RequestHandler) zip.Server {
//		return myquic.NewServer(addr, h)
//	})
func RegisterTransport(scheme string, tf TransportFunc) {
	transportsMu.Lock()
	defer transportsMu.Unlock()
	transports[scheme] = tf
}

// prepare installs the deferred projections (OpenAPI doc + MCP tool surface)
// before any listener starts, so every transport exposes the same routes. Runs
// exactly once even if Listen is called again.
func (a *App) prepare() {
	a.prepareOnce.Do(func() {
		a.installOpenAPIRoutes()
		a.installMCP()
	})
}

// Listen serves the app on one or more addresses and blocks until all
// listeners stop or the first one errors. The address scheme selects the
// transport; a bare address uses ZAP (DefaultScheme). This is the ONE and only
// way to serve a zip app — no per-transport methods.
func (a *App) Listen(addrs ...string) error {
	if len(addrs) == 0 {
		return fmt.Errorf("zip: Listen needs at least one address")
	}
	a.prepare()
	h := a.fiber.Handler()

	servers := make([]Server, 0, len(addrs))
	for _, raw := range addrs {
		scheme, addr := splitScheme(raw)
		transportsMu.RLock()
		tf, ok := transports[scheme]
		transportsMu.RUnlock()
		if !ok {
			return fmt.Errorf("zip: no transport registered for scheme %q (address %q)", scheme, raw)
		}
		servers = append(servers, tf(addr, h))
		a.logger.Info("zip listening", "transport", scheme, "addr", addr)
	}

	a.srvMu.Lock()
	a.servers = servers
	a.srvMu.Unlock()

	// Serve every transport concurrently; return the first error (Shutdown
	// closes the rest via closeServers).
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func(s Server) { errc <- s.ListenAndServe() }(s)
	}
	return <-errc
}

// closeServers stops every running listener. Called from Shutdown.
func (a *App) closeServers() {
	a.srvMu.Lock()
	servers := a.servers
	a.srvMu.Unlock()
	for _, s := range servers {
		_ = s.Close()
	}
}

// splitScheme splits "scheme://addr" into (scheme, addr); a bare address
// (no "://") yields (DefaultScheme, addr).
func splitScheme(raw string) (scheme, addr string) {
	if i := strings.Index(raw, "://"); i >= 0 {
		return raw[:i], raw[i+3:]
	}
	return DefaultScheme, raw
}

// httpServer adapts fasthttp.Server (whose ListenAndServe takes the addr) to
// the Server interface (whose ListenAndServe takes none) so plain HTTP is just
// another transport in the registry.
type httpServer struct {
	addr string
	srv  *fasthttp.Server
}

func (h *httpServer) ListenAndServe() error { return h.srv.ListenAndServe(h.addr) }
func (h *httpServer) Close() error          { return h.srv.Shutdown() }
