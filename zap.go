package zip

import (
	"github.com/gofiber/fiber/v3"
	zaphttp "github.com/zap-proto/http"
)

// The transport layer: ONE fiber handler, served over TWO transports.
//
// ZAP (TLS 1.3 + post-quantum, gRPC's replacement) is the PRIMARY transport;
// plain HTTP is the optional EXTRA for human/REST/browser clients. Both serve
// the SAME `a.fiber.Handler()`, so every /v1 route is reachable identically
// over either — there is NO separate RPC surface to register, NO ordinal mux,
// NO per-endpoint wiring. This is the one-and-only-one-way model: your routes
// ARE the ZAP surface, exactly as they are the HTTP surface. (Supersedes the
// old zaprpc-registry ZAPListen stub — the routes replace the registry.)

// prepare installs the deferred routes (OpenAPI) before a listener starts.
// Shared by both transports so ZAP and HTTP expose the same surface.
func (a *App) prepare() { a.installOpenAPIRoutes() }

// ListenHTTP serves plain HTTP on addr and blocks. The optional EXTRA
// transport. (Was `Listen`; renamed for symmetry with ListenZAP — verb-first,
// transport-suffixed, the way Go stdlib names net.Listen/http.ListenAndServe.)
func (a *App) ListenHTTP(addr string) error {
	a.prepare()
	a.logger.Info("zip listening HTTP", "addr", addr)
	return a.fiber.Listen(addr, fiber.ListenConfig{
		DisableStartupMessage: a.cfg.DisableStartupMessage,
	})
}

// ListenZAP serves the fiber handler over ZAP — the PRIMARY transport. Blocks
// until Shutdown. Every route registered on this App answers over ZAP with the
// same middleware, auth filters, and error handling as over HTTP.
func (a *App) ListenZAP(addr string) error {
	a.prepare()
	s := &zaphttp.Server{Addr: addr, Handler: a.fiber.Handler()}
	a.zapMu.Lock()
	a.zap = s
	a.zapMu.Unlock()
	a.logger.Info("zip listening ZAP", "addr", addr)
	return s.ListenAndServe()
}

// Serve runs ZAP (primary) and, when httpAddr != "", HTTP (extra) concurrently,
// returning the first listener error. The normal way to bring a zip binary up
// on both transports.
func (a *App) Serve(zapAddr, httpAddr string) error {
	errc := make(chan error, 2)
	go func() { errc <- a.ListenZAP(zapAddr) }()
	if httpAddr != "" {
		go func() { errc <- a.ListenHTTP(httpAddr) }()
	}
	return <-errc
}

// closeZAP stops the ZAP listener if one is running. Called from Shutdown.
func (a *App) closeZAP() {
	a.zapMu.Lock()
	s := a.zap
	a.zapMu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}
