package zip

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/http"
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
// request. *http.Transport and *fasthttp.HostClient already satisfy it, so
// giving a scheme a Dial is a one-liner.
type Client interface {
	Do(req *fasthttp.Request, resp *fasthttp.Response) error
}

// Transport is one address scheme in both directions: Serve terminates bytes
// arriving here, Dial originates bytes going there. They are the same concept
// — a wire — so they live in one value rather than two registries that drift.
// A scheme may leave either half nil; Listen and Proxy each say which they
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
				return &http.Server{Network: NetworkOf(addr), Addr: addr, Handler: h}
			},
			Dial: func(addr string) Client { return http.Dial(NetworkOf(addr), addr) },
		},
		"http": {
			Serve: func(addr string, h fasthttp.RequestHandler) Server {
				return &httpServer{addr: addr, srv: &fasthttp.Server{Handler: h}}
			},
			Dial: func(addr string) Client {
				return &fasthttp.HostClient{Addr: withPort(addr, "80"), DisablePathNormalizing: true}
			},
		},
		// DisablePathNormalizing on both dialers: the address a caller BUILT is the
		// address that goes out. fasthttp's default is to re-read the path —
		// decoding %2F and resolving ".." — which turns a percent-encoded argument
		// back into path structure and lets one operation's input address another
		// (see Remote.do). HostClient.doNonNilReqResp assigns the request's flag
		// FROM this field, so on these two transports this is the only setting that
		// is read; the request-level one covers the rest.
		//
		// Dial-only: terminating TLS is the ingress's job, so nothing in the
		// fleet serves https directly — but reaching something that does (an
		// api.* host, a third party) is an ordinary Proxy, and a CLI talking to
		// a published API is the same call. One registry entry, both directions
		// covered by the half that exists.
		"https": {
			Dial: func(addr string) Client {
				return &fasthttp.HostClient{Addr: withPort(addr, "443"), IsTLS: true, DisablePathNormalizing: true}
			},
		},
	}
)

// withPort defaults the port of a tcp address, because a URL usually omits it
// and a dialer never can. A unix path is returned untouched.
func withPort(addr, port string) string {
	if NetworkOf(addr) == "unix" || strings.LastIndex(addr, ":") > strings.LastIndex(addr, "]") {
		return addr
	}
	return addr + ":" + port
}

// RegisterTransport adds (or replaces) a transport keyed by address scheme, so
// any future protocol slots into both Listen and Proxy with ZERO change to
// either API. Call before Listen or Proxy.
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

// NetworkOf reads the plumbing off the address shape, the way the address
// already tells you: a filesystem path is a unix socket, anything else is
// host:port. The scheme names the PROTOCOL (zap) and the address names where
// it is spoken, so there is no second scheme for the same wire.
//
//	zap://:9653                      -> tcp
//	zap://billing.hanzo.svc:9653     -> tcp
//	zap:///run/hanzo/billing.sock    -> unix
//	/run/hanzo/billing.sock          -> unix (bare address, DefaultScheme)
//	@hanzo-billing                   -> unix (Linux abstract socket)
//
// It is exported because it is a RULE and not an implementation detail: anything
// that dials one of these addresses has to make the same reading, and a second
// copy of it is a place where a socket path becomes a hostname. [zapmcp.Dial]
// wants the network as a value, so a client reaching an app's MCP door asks
// here rather than re-deriving it.
func NetworkOf(addr string) string {
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
// OpenAPI document, the GraphQL schema, the MCP tool surface, the op-call
// plane) before any listener starts, so every transport exposes the same
// routes. Runs exactly once even if Listen is called again.
func (a *App) prepare() {
	a.prepareOnce.Do(func() {
		a.installOpenAPIRoutes()
		a.installGraph()
		a.installMCP()
		a.installCallPlane()
		a.installPluginRoute()
		a.prepared.Store(true)
	})
}

// Listen is [Serve] followed by [Host.Wait]: it starts serving and blocks until
// the listeners stop. The address scheme selects the transport; a bare address
// uses ZAP (DefaultScheme).
//
// It is the TERMINAL spelling, and the difference from Serve is deliberate
// rather than an oversight: Listen hands back no host, so a program started this
// way cannot later be changed with [Host.Include] or [Host.Drop]. That is the
// right shape for a main whose program is fixed —
//
//	log.Fatal(app.Listen(":8080"))
//
// — and the wrong one the moment anything loads at run time, which is why Serve
// exists and why this is one line of sugar over it rather than a second way to
// serve. There is one serving mechanism; this is its no-handle form.
func (a *App) Listen(addrs ...string) error {
	h, err := Serve(a, addrs...)
	if err != nil {
		return err
	}
	return h.Wait()
}

// listenOn serves an ALREADY BUILT program. [Serve] uses it after installing
// generation 0, so the build happens once and in one place.
func (a *App) listenOn(addrs []string) error {
	// The handler resolves the generation PER REQUEST, so a later Include or
	// Drop is picked up without re-binding a listener, and a request that has
	// already arrived finishes on the generation it arrived under.
	h := a.pinned()

	// Asked BEFORE anything binds, so a door that cannot be honoured costs a
	// boot and not a deployment: nothing is listening to take traffic that would
	// silently miss half its tools.
	if err := a.checkMCP(); err != nil {
		return err
	}

	servers := make([]Server, 0, len(addrs))
	unbind := make([]func(), 0, len(addrs))
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
		// The plain-HTTP listener that lets an upgrade reach this app. [plain]
		// derives its address and the host dials it; this is the half that
		// answers. Appended to servers so it shuts down with the rest, and NOT
		// bound: it is the same app at a derived address, not a second place to
		// find it.
		// The sibling's address is DERIVED, so its length is not something the
		// caller chose and not something it can see. Asked here, where an error can
		// still be returned, because the alternative is what it used to be: the bind
		// fails inside the transport as "invalid argument", which names neither the
		// limit nor the length, and reads like a broken socket rather than a long
		// path. load.go asks the same question of the same address before it execs a
		// child; this is the other half of one rule.
		if to := plain(addr); to != "" {
			if err := socketFits(to); err != nil {
				return fmt.Errorf("zip: the upgrade sibling cannot be bound: %w", err)
			}
		}
		if p := a.plainSibling(addr, h); p != nil {
			servers = append(servers, p)
		}
		// This app now answers at this address, and a caller in THIS process can
		// reach its ops without the address (see [Serving] and [Here]). Recorded
		// here rather than at Serve because binding is what makes it true.
		unbind = append(unbind, bound(addr, a))
		a.logger.Info("zip listening", "transport", scheme, "addr", addr)
	}

	a.srvMu.Lock()
	a.servers = servers
	a.unbind = unbind
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
	// MCP over ZAP, when the deployment named an address for it. It is not a
	// scheme in the transport registry above, because that registry is about
	// WIRES that carry fasthttp requests and this is a different protocol on the
	// same wire — the sibling of zap-proto/http, not a second spelling of it.
	a.serveMCP()

	// Serve every transport concurrently; return the first error (Shutdown
	// closes the rest via closeServers).
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func(s Server) { errc <- s.ListenAndServe() }(s)
	}
	return <-errc
}

// mount registers prefix onto a dialled address. The prefix is already CLAIMED
// by the caller — [Proxy] claims its one, [App.load] claims a plugin's whole set
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
		if upgrading(c.fc.Request()) {
			return relay(c.fc.RequestCtx(), host, "mount "+prefix)
		}
		return forward(c.fc.Request(), c.fc.Response(), client, host, "", "mount "+prefix)
	}
	prefix = strings.TrimSuffix(normPath(prefix), "/")
	site := here(1)
	// runtimeEntry, because a plugin may be loaded AFTER Listen: a runtime mount
	// is a deployment event, not a program edit, so it goes onto the live router
	// without reopening a sealed program.
	// A plugin may be mounted AFTER Listen, so this goes through a generation
	// when one is live: build the new route set, validate it, swap or keep
	// serving the old one.
	if err := a.includeRoutes(site,
		route{method: methodAll, path: prefix, chain: []Handler{h}},
		route{method: methodAll, path: prefix + "/*", chain: []Handler{h}},
	); err != nil {
		a.logger.Error("zip mount refused", "prefix", prefix, "err", err)
	}
}

// forward is THE proxy hop: the inbound fasthttp request object is handed to the
// client and the reply written straight back, so nothing is re-encoded in
// between. A non-empty path rewrites the target — which is the whole difference
// between a mounted prefix (same path) and the composed MCP door (the owner's own
// /mcp) — so there is one forwarder in the framework and not two.
//
// what names the caller in the error, because "no instance running" is worth
// nothing without knowing what did not run.
func forward(req *fasthttp.Request, resp *fasthttp.Response, client Client, host, path, what string) error {
	if client == nil {
		return Errorf(503, "%s: no instance running", what)
	}
	// The caller's Host survives the hop. A client dials its own address and never
	// reads this header to find the far end, so overwriting it destroyed the one
	// fact a callee needs to serve more than one brand. The dial address is the
	// fallback for a request carrying none — HTTP/1.1 requires the header.
	if inbound := req.Header.Host(); len(inbound) > 0 {
		host = string(inbound)
	}
	req.SetHost(host)
	if path != "" {
		req.URI().SetPath(path)
	}
	if err := client.Do(req, resp); err != nil {
		// The upstream, not this hop, is what failed.
		return Errorf(502, "%s: %v", what, err)
	}
	return nil
}

// upgradeWait bounds reaching a plugin for an upgrade. It is a dial on a socket
// in this pod, so a second is already generous; what it exists to stop is a
// caller hanging on a plugin that is not listening yet.
const upgradeWait = time.Second

// plain is where a mounted plugin speaks ordinary HTTP, derived from the address
// it speaks ZAP on.
//
// A plugin's socket carries ZAP frames: one length-prefixed request, one
// length-prefixed reply. That shape has no room for a connection that STOPS being
// request-and-reply, which is exactly what an upgrade is — so a websocket cannot
// cross it, and the frames a client sends after the handshake arrive at an HTTP
// parser as nonsense ("error when reading request headers", the first byte being
// 0x81). The plugin therefore listens a second time, in plain HTTP, beside the
// first. This DERIVES that address instead of configuring it, so a mount is still
// one address and there is no second value to keep in step with the first.
//
// Only over a unix socket, because that is the one place the sibling is known to
// be the same process. A tcp mount would need a port nobody has agreed on.
func plain(addr string) string {
	if NetworkOf(addr) != "unix" {
		return ""
	}
	return addr + ".http"
}

// plainSibling is the listener that answers at [plain] — the second, ordinary
// HTTP listener a mounted app needs because an upgrade cannot cross ZAP framing.
// Without it the host dials a socket nobody created and every websocket through
// a mount fails the handshake.
//
// nil when the address has no sibling. A tcp address has none (no port both ends
// have agreed on), and neither does an app that is ITSELF a sibling — ops and
// peer are reached by name from inside the fleet, never mounted, so a second
// socket beside them would be one nothing dials.
func (a *App) plainSibling(addr string, h fasthttp.RequestHandler) Server {
	if a.sibling {
		return nil
	}
	to := plain(addr)
	if to == "" {
		return nil
	}
	// Resolved through the registry rather than constructed here, so the sibling
	// is the same HTTP transport a caller gets from an http:// address and there
	// is one implementation of "serve plain HTTP" to keep correct.
	_, at, t, err := transportFor("http://" + to)
	if err != nil || t.Serve == nil {
		a.logger.Error("zip: no http transport for the upgrade sibling", "addr", to, "err", err)
		return nil
	}
	s := t.Serve(at, h)
	if t, ok := s.(tunableServer); ok {
		t.applyConfig(a.cfg)
	}
	a.logger.Info("zip listening", "transport", "http", "addr", at, "for", "upgrade")
	return s
}

// upgrading reports whether a request is asking to stop being HTTP.
func upgrading(req *fasthttp.Request) bool {
	return len(req.Header.Peek(fasthttp.HeaderUpgrade)) > 0 &&
		bytes.Contains(bytes.ToLower(req.Header.Peek(fasthttp.HeaderConnection)),
			[]byte("upgrade"))
}

// relay carries an upgraded connection between a caller and a mounted plugin.
//
// Everything up to the switch is ordinary — the request goes up as it arrived,
// and a plugin that declines (an unauthorized socket is refused BEFORE it opens)
// answers an ordinary reply that comes straight back. What is different is what
// happens after 101: neither end is speaking HTTP any more, so there is nothing
// left to parse and the only correct act is to move bytes until one side stops.
//
// The switch reply is written verbatim, from the bytes the plugin sent, rather
// than through the response object — fasthttp would frame a body onto a message
// that has none. Bytes the plugin has already sent past the header are held in
// the reader, so the copy starts there and not at the socket.
func relay(rc *fasthttp.RequestCtx, addr, what string) error {
	to := plain(addr)
	if to == "" {
		return Errorf(502, "%s: no upgrade path over %s", what, NetworkOf(addr))
	}
	up, err := net.DialTimeout(NetworkOf(to), to, upgradeWait)
	if err != nil {
		return Errorf(502, "%s: upgrade: %v", what, err)
	}
	bw := bufio.NewWriter(up)
	if err := rc.Request.Write(bw); err == nil {
		err = bw.Flush()
	}
	if err != nil {
		_ = up.Close()
		return Errorf(502, "%s: upgrade: %v", what, err)
	}

	br := bufio.NewReader(up)
	var head fasthttp.ResponseHeader
	if err := head.Read(br); err != nil {
		_ = up.Close()
		return Errorf(502, "%s: upgrade: %v", what, err)
	}
	if head.StatusCode() != fasthttp.StatusSwitchingProtocols {
		defer func() { _ = up.Close() }()
		head.CopyTo(&rc.Response.Header)
		body, err := body(br, head.ContentLength())
		if err != nil {
			return Errorf(502, "%s: upgrade: %v", what, err)
		}
		rc.Response.SetBody(body)
		return nil
	}

	switched := head.Header()
	rc.HijackSetNoResponse(true)
	rc.Hijack(func(down net.Conn) {
		defer func() { _ = up.Close() }()
		defer func() { _ = down.Close() }()
		if _, err := down.Write(switched); err != nil {
			return
		}
		go func() {
			_, _ = io.Copy(up, down)
			// The caller is gone. Close write so the plugin's read sees EOF and
			// ends its session rather than holding a shell nobody is watching.
			if c, ok := up.(interface{ CloseWrite() error }); ok {
				_ = c.CloseWrite()
			}
		}()
		_, _ = io.Copy(down, br)
	})
	return nil
}

// body reads a reply's body given the length its header declared. A negative
// length is "until the connection ends", which is what identity encoding means.
func body(br *bufio.Reader, n int) ([]byte, error) {
	if n < 0 {
		return io.ReadAll(br)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		return nil, err
	}
	return b, nil
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
	if NetworkOf(addr) != "unix" || strings.HasPrefix(addr, "@") {
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
	unbind := a.unbind
	a.unbind = nil
	a.srvMu.Unlock()
	// Stop answering by name BEFORE the listeners go, so an in-process caller
	// cannot be handed an App that is on its way down while a caller over the
	// socket is already getting a refused connection. The two ends of the same
	// app must not disagree about whether it is up.
	for _, u := range unbind {
		u()
	}
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

// ListenAndServe binds the address, over tcp or over a unix socket.
//
// A PATH IS A SOCKET on this transport for the same reason it is on the ZAP one:
// the address names where the bytes are spoken and the shape of the address says
// which kind of place that is. It earns its keep next to a plugin — an upgraded
// connection cannot cross ZAP framing, so a plugin listens HTTP beside its socket
// and the host relays to it — and a path there must not mean "a tcp host called
// /var/lib/…", which is what binding it as tcp amounts to.
func (h *httpServer) ListenAndServe() error {
	if NetworkOf(h.addr) == "unix" {
		return h.srv.ListenAndServeUNIX(h.addr, 0o600)
	}
	return h.srv.ListenAndServe(h.addr)
}
func (h *httpServer) Close() error { return h.srv.Shutdown() }

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
	// The body ceiling reaches the wire only here. fiber binds its own BodyLimit
	// when fiber owns the listener, and on this transport it does not, so without
	// this the socket keeps fasthttp's 4 MiB default. fasthttp refuses an
	// oversized body before any handler runs, answering 400 "Error when parsing
	// request".
	if cfg.BodyLimit > 0 {
		h.srv.MaxRequestBodySize = cfg.BodyLimit
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
