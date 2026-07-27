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
//	app.Listen("/run/hanzo/app.sock")    // ZAP on a unix socket
//	app.Listen("quic://:443")            // any RegisterTransport'd proto
//
// This mirrors net.Listen(network, addr): the network is a value, not a
// ListenTCP/ListenUDP method explosion. The scheme names the PROTOCOL and the
// address names where it is spoken — a path is a unix socket, a host:port is
// tcp — so there is never a second scheme for the same wire.

// Server is a running transport listener bound to one address. Both
// zap-proto/http.Server and the built-in HTTP server satisfy it, as does any
// custom transport.
type Server interface {
	ListenAndServe() error
	Close() error
}

// Client is the call side of a transport: anything that can complete a
// request. *zaphttp.Transport and *fasthttp.HostClient already satisfy it, so
// giving a scheme a Dial is a one-liner.
type Client interface {
	Do(req *fasthttp.Request, resp *fasthttp.Response) error
}

// Transport is one address scheme in both directions: Serve terminates bytes
// arriving here, Dial originates bytes going there. They are the same concept
// — a wire — so they live in one value rather than two registries that drift.
// A scheme may leave either half nil; Listen and Mount each say which they
// needed.
type Transport struct {
	Serve func(addr string, handler fasthttp.RequestHandler) Server
	Dial  func(addr string) Client
}

// DefaultScheme is the transport a bare address (no "scheme://") uses. ZAP is
// the primary transport (TLS 1.3 + post-quantum, gRPC's replacement), so the
// path of least resistance is ZAP-native.
const DefaultScheme = "zap"

var (
	transportsMu sync.RWMutex
	transports   = map[string]Transport{
		"zap": {
			Serve: func(addr string, h fasthttp.RequestHandler) Server {
				return &zaphttp.Server{Network: networkOf(addr), Addr: addr, Handler: h}
			},
			Dial: func(addr string) Client { return zaphttp.Dial(networkOf(addr), addr) },
		},
		"http": {
			Serve: func(addr string, h fasthttp.RequestHandler) Server {
				return &httpServer{addr: addr, srv: &fasthttp.Server{Handler: h}}
			},
			Dial: func(addr string) Client { return &fasthttp.HostClient{Addr: addr} },
		},
	}
)

// RegisterTransport adds (or replaces) a transport keyed by address scheme, so
// any future protocol slots into both Listen and Mount with ZERO change to
// either API. Call before Listen or Mount.
//
//	zip.RegisterTransport("quic", zip.Transport{
//		Serve: func(addr string, h fasthttp.RequestHandler) zip.Server {
//			return myquic.NewServer(addr, h)
//		},
//		Dial: func(addr string) zip.Client { return myquic.NewClient(addr) },
//	})
func RegisterTransport(scheme string, t Transport) {
	transportsMu.Lock()
	defer transportsMu.Unlock()
	transports[scheme] = t
}

// networkOf reads the plumbing off the address shape, the way the address
// already tells you: a filesystem path is a unix socket, anything else is
// host:port. The scheme names the PROTOCOL (zap) and the address names where
// it is spoken, so there is no second scheme for the same wire.
//
//	zap://:9653                      -> tcp
//	zap://billing.hanzo.svc:9653     -> tcp
//	zap:///run/hanzo/billing.sock    -> unix
//	/run/hanzo/billing.sock          -> unix (bare address, DefaultScheme)
//	@hanzo-billing                   -> unix (Linux abstract socket)
func networkOf(addr string) string {
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") || strings.HasPrefix(addr, "@") {
		return "unix"
	}
	return "tcp"
}

// transportFor resolves a raw address to its scheme, bare address, and wire.
func transportFor(raw string) (scheme, addr string, t Transport, err error) {
	scheme, addr = splitScheme(raw)
	transportsMu.RLock()
	t, ok := transports[scheme]
	transportsMu.RUnlock()
	if !ok {
		return scheme, addr, t, fmt.Errorf("zip: no transport registered for scheme %q (address %q)", scheme, raw)
	}
	return scheme, addr, t, nil
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
		scheme, addr, t, err := transportFor(raw)
		if err != nil {
			return err
		}
		if t.Serve == nil {
			return fmt.Errorf("zip: transport %q cannot serve (dial-only)", scheme)
		}
		s := t.Serve(addr, h)
		// Push the App's per-conn wire tuning (ReadBufferSize / WriteBufferSize /
		// Concurrency) into the transport's fasthttp.Server. Without this the
		// built-in HTTP transport constructs a bare fasthttp.Server whose
		// ReadBufferSize defaults to 4 KiB — capping total request-header size and
		// returning 431 (Request Header Fields Too Large) above it, which silently
		// dropped every zip.Config buffer knob at the wire. The transport, not just
		// the fiber handler, must honor Config. A custom transport opts in by
		// implementing tunableServer.
		if t, ok := s.(tunableServer); ok {
			t.applyConfig(a.cfg)
		}
		servers = append(servers, s)
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

// Mount delegates every request under prefix to the service at addr. Listen
// serves here; Mount delegates there — one registry, one scheme vocabulary,
// opposite directions. A bare address uses ZAP (DefaultScheme), so a
// colocated service is mounted by address alone:
//
//	app.Mount("/v1/billing", "billing.hanzo.svc:9653")      // ZAP
//	app.Mount("/v1/legacy",  "http://legacy.internal:8080") // HTTP
//
// The mounted service keeps the whole path — /v1/billing/invoices arrives as
// /v1/billing/invoices — so its routes ARE the surface, exactly as they are
// when it is linked in. Nothing is re-encoded in between: the inbound
// fasthttp request object is handed to the client and the reply is written
// straight back, so a mount costs a connection, not a copy.
//
// This is how a service ships as its own binary. The mounting binary links
// zip and a transport, never the mounted service's dependency graph, so its
// link time does not grow when that service does, and the service redeploys
// without relinking it. Moving a service between Mount and a linked-in route
// tree is a deployment decision, not a code change.
func (a *App) Mount(prefix, addr string) error {
	scheme, hostport, t, err := transportFor(addr)
	if err != nil {
		return err
	}
	if t.Dial == nil {
		return fmt.Errorf("zip: transport %q cannot dial (serve-only)", scheme)
	}
	client := t.Dial(hostport)
	a.logger.Info("zip mounting", "prefix", prefix, "transport", scheme, "addr", hostport)

	h := func(c *Ctx) error {
		req, resp := c.fc.Request(), c.fc.Response()
		req.SetHost(hostport)
		if err := client.Do(req, resp); err != nil {
			// The upstream, not this hop, is what failed.
			return Errorf(502, "mount %s: %v", prefix, err)
		}
		return nil
	}

	prefix = strings.TrimSuffix(normPath(prefix), "/")
	a.All(prefix, h)
	a.All(prefix+"/*", h)
	return nil
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

// tunableServer is a transport whose underlying server accepts the App's
// per-conn wire tuning. Listen applies it after construction so zip.Config's
// buffer/concurrency knobs actually reach the socket. The built-in HTTP
// transport implements it; a custom transport may too, or ignore the config.
type tunableServer interface{ applyConfig(cfg Config) }

// applyConfig copies the App's fasthttp tuning onto the HTTP transport's
// server. Only non-zero knobs are applied, so an unset field falls through to
// fasthttp's own default (4 KiB buffers, 256k concurrency) rather than zeroing
// it. This is the seam that makes zip.Config{ReadBufferSize: 32768} raise the
// 431 header ceiling on the wire.
func (h *httpServer) applyConfig(cfg Config) {
	if cfg.ReadBufferSize > 0 {
		h.srv.ReadBufferSize = cfg.ReadBufferSize
	}
	if cfg.WriteBufferSize > 0 {
		h.srv.WriteBufferSize = cfg.WriteBufferSize
	}
	if cfg.Concurrency > 0 {
		h.srv.Concurrency = cfg.Concurrency
	}
	// The transport's OWN pre-routing responses — 431 (header overflow), 400
	// (parse error), request timeouts — are written by fasthttp BEFORE the fiber
	// handler runs, so no middleware (including ProductionHeaders) can brand
	// them. Left unset, fasthttp stamps its framework default "Server: fasthttp"
	// — a stack leak on exactly the malformed-request path an attacker probes.
	// Propagate the App's ServerHeader onto the transport server so those
	// responses carry the same value as the handled ones; "-" suppresses the
	// header entirely (NoDefaultServerHeader), matching New()'s suppress semantic.
	switch cfg.ServerHeader {
	case "-":
		h.srv.NoDefaultServerHeader = true
	case "":
		// Unreachable via New (which defaults to "zip"), but a direct transport
		// caller with no ServerHeader still must not leak "fasthttp".
		h.srv.NoDefaultServerHeader = true
	default:
		h.srv.Name = cfg.ServerHeader
	}
}
