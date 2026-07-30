package zip

import (
	"fmt"
	"os"
	"path/filepath"
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
			Dial: func(addr string) Client { return &fasthttp.HostClient{Addr: withPort(addr, "80")} },
		},
		// Dial-only: terminating TLS is the ingress's job, so nothing in the
		// fleet serves https directly — but reaching something that does (an
		// api.* host, a third party) is an ordinary Mount, and a CLI talking to
		// a published API is the same call. One registry entry, both directions
		// covered by the half that exists.
		"https": {
			Dial: func(addr string) Client {
				return &fasthttp.HostClient{Addr: withPort(addr, "443"), IsTLS: true}
			},
		},
	}
)

// withPort defaults the port of a tcp address, because a URL usually omits it
// and a dialer never can. A unix path is returned untouched.
func withPort(addr, port string) string {
	if networkOf(addr) == "unix" || strings.LastIndex(addr, ":") > strings.LastIndex(addr, "]") {
		return addr
	}
	return addr + ":" + port
}

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

// prepare installs the deferred projections of the typed-op registry (the
// OpenAPI document, the MCP tool surface, the op-call plane) before any
// listener starts, so every transport exposes the same routes. Runs exactly
// once even if Listen is called again.
func (a *App) prepare() {
	a.prepareOnce.Do(func() {
		a.installOpenAPIRoutes()
		a.installMCP()
		a.installCallPlane()
		a.installPluginRoute()
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
		if err := makeSocketDir(addr); err != nil {
			return err
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
	a.listening.Add(int32(len(servers)))

	// The two listeners a plugin main does not have to write. Each appears only
	// when the app actually has the thing it serves, so one verb — Listen —
	// covers a plugin run standalone and the same plugin run as a child.
	//
	// HIP-0119 §1: the ops surface is a SECOND listener, up only when the
	// deployment named an ops port (a host names none on a child).
	a.serveOps()
	// HIP-0106 §2(3): peer ops ride a SECOND app served only on the canonical
	// socket, so an internal op is never reachable on the edge.
	a.servePeer()

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
	// A bare Mount is its own claimant, and the address is the only name it has.
	if err := a.claim(prefix, addr); err != nil {
		return err
	}
	return a.mount(prefix, addr)
}

// mount registers prefix onto a dialled address. The prefix is already CLAIMED
// by the caller — [Mount] claims its one, [App.load] claims a plugin's whole set
// all-or-nothing before spawning anything — so this does not claim again and
// there is exactly one claim per prefix however the composition came in.
func (a *App) mount(prefix, addr string) error {
	scheme, hostport, t, err := transportFor(addr)
	if err != nil {
		return err
	}
	if t.Dial == nil {
		return fmt.Errorf("zip: transport %q cannot dial (serve-only)", scheme)
	}
	client := t.Dial(hostport)
	a.logger.Info("zip mounting", "prefix", prefix, "transport", scheme, "addr", hostport)
	a.mountVia(prefix, func() (Client, string) { return client, hostport })
	return nil
}

// mountVia registers prefix once, resolving the target per request. The
// indirection is what makes a plugin reloadable: a reload swaps what `to`
// returns, so the router is never touched twice. Re-registering routes on
// every reload would grow the route table without bound — the leak that makes
// naive hot-reload untenable.
//
// Registering the route is a separate concern from CLAIMING the prefix (see
// [App.claim]): a reload re-resolves the target without re-registering, and an
// ownership assertion happens once, at the door the composition came in.
func (a *App) mountVia(prefix string, to func() (Client, string)) {
	h := func(c *Ctx) error {
		client, host := to()
		if client == nil {
			return Errorf(503, "mount %s: no instance running", prefix)
		}
		req, resp := c.fc.Request(), c.fc.Response()
		req.SetHost(host)
		if err := client.Do(req, resp); err != nil {
			// The upstream, not this hop, is what failed.
			return Errorf(502, "mount %s: %v", prefix, err)
		}
		return nil
	}
	prefix = strings.TrimSuffix(normPath(prefix), "/")
	a.All(prefix, h)
	a.All(prefix+"/*", h)
}

// claim records owner as the holder of prefix, or refuses because someone else
// already holds it. The comparison is on the NORMALISED prefix, so "/v1/x" and
// "/v1/x/" are one claim and not two.
//
// This catches an exact duplicate at compose time, with a message naming both
// claimants. It does NOT catch SHADOWING — a parameter sibling swallowing
// another plugin's deeper static path, which is sometimes legitimate — which is
// why a host also runs the router oracle over the composed union (HIP-0106
// §3.5). Two checks, two distinct failures, not two ways to do one thing.
func (a *App) claim(prefix, owner string) error {
	key := strings.TrimSuffix(normPath(prefix), "/")
	a.plugMu.Lock()
	defer a.plugMu.Unlock()
	if held, dup := a.claims[key]; dup {
		return fmt.Errorf("zip: %q is already claimed by %q — %q cannot have it too", key, held, owner)
	}
	if a.claims == nil {
		a.claims = map[string]string{}
	}
	a.claims[key] = owner
	return nil
}

// release drops a claim, so Unload frees the prefix its plugin held and a
// replacement can take it. Without this an Unload+Load cycle refuses its own
// second half.
func (a *App) release(prefixes []string) {
	a.plugMu.Lock()
	defer a.plugMu.Unlock()
	for _, p := range prefixes {
		delete(a.claims, strings.TrimSuffix(normPath(p), "/"))
	}
}

// makeSocketDir creates the directory a unix socket will be bound in, so
// serving at the canonical zip.SocketPath("name") works on a host where the
// runtime dir does not exist yet — which is every host, the first time.
//
// 0700, so the fleet's sockets are reachable only by the user running them.
// An existing directory keeps its own mode: a deployment that needs a socket
// shared across users creates the directory itself, with the mode it means,
// and points ZIP_RUNTIME_DIR at it. An abstract socket (@name) has no
// directory and a tcp address has no path.
func makeSocketDir(addr string) error {
	if networkOf(addr) != "unix" || strings.HasPrefix(addr, "@") {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
		return fmt.Errorf("zip: socket dir for %s: %w", addr, err)
	}
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
