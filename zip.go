// Package zip is the ZAP-native web framework for every Lux/Hanzo Go binary
// (all nodes — luxd/hanzod/zood/parsd — the gateway, and all cloud services).
//
// One framework, one way: gofiber v3 for routing + middleware, served over ZAP
// as the PRIMARY transport, with a plain-HTTP listener as an optional extra.
// Because the transport is ZAP, ZAP + MCP ride on top natively for free.
//
//	a := zip.New()
//	a.Fiber().Get("/v1/health", healthz)     // static routes + middleware
//	a.MountFast("/v1/bc/C", chainHandler)     // runtime chain mounts (post-Listen)
//	a.Serve(":9632", ":9630")                 // ZAP primary + HTTP extra
package zip

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	zaphttp "github.com/zap-proto/http"
)

// Config aliases fiber.Config so the router is configured in one place.
type Config = fiber.Config

// Handler aliases the fiber v3 handler (func(fiber.Ctx) error).
type Handler = fiber.Handler

// App is a ZAP-native web application.
type App struct {
	fiber *fiber.App

	mu  sync.RWMutex
	dyn []dynRoute // longest-prefix first
	zap *zaphttp.Server
}

type dynRoute struct {
	prefix string
	h      fasthttp.RequestHandler
}

// New builds an App. Optional Config passes straight through to fiber.
func New(cfg ...Config) *App {
	a := &App{}
	a.fiber = fiber.New(cfg...)
	// Runtime dynamic dispatch, ahead of the static routes: longest-prefix match
	// over a mutable registry. This is how a node registers chain handlers AFTER
	// the listeners are already serving — Fiber's static tree can't take routes
	// post-Listen, this registry can.
	a.fiber.Use(func(c fiber.Ctx) error {
		if h := a.match(c.Path()); h != nil {
			h(c.RequestCtx())
			return nil
		}
		return c.Next()
	})
	return a
}

func (a *App) match(path string) fasthttp.RequestHandler {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for i := range a.dyn { // sorted longest-prefix first
		if strings.HasPrefix(path, a.dyn[i].prefix) {
			return a.dyn[i].h
		}
	}
	return nil
}

// Fiber exposes the underlying router for static Get/Post/Group/Use registration.
func (a *App) Fiber() *fiber.App { return a.fiber }

// Handler is the fasthttp handler both listeners serve.
func (a *App) Handler() fasthttp.RequestHandler { return a.fiber.Handler() }

// Mount attaches a net/http.Handler at prefix. VM and chain handlers are
// net/http — this is the ONE net/http boundary, adapted to fasthttp.
func (a *App) Mount(prefix string, h http.Handler) {
	a.MountFast(prefix, fasthttpadaptor.NewFastHTTPHandler(h))
}

// MountFast registers/replaces a fasthttp handler at prefix. Safe to call at
// runtime after the listeners are serving. Longest matching prefix wins.
func (a *App) MountFast(prefix string, h fasthttp.RequestHandler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.dyn {
		if a.dyn[i].prefix == prefix {
			a.dyn[i].h = h
			return
		}
	}
	a.dyn = append(a.dyn, dynRoute{prefix: prefix, h: h})
	sort.Slice(a.dyn, func(i, j int) bool { return len(a.dyn[i].prefix) > len(a.dyn[j].prefix) })
}

// Unmount removes a mounted prefix.
func (a *App) Unmount(prefix string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.dyn[:0]
	for _, r := range a.dyn {
		if r.prefix != prefix {
			out = append(out, r)
		}
	}
	a.dyn = out
}

// ListenZAP serves over ZAP — the PRIMARY transport. Blocks until Shutdown.
func (a *App) ListenZAP(addr string) error {
	s := &zaphttp.Server{Addr: addr, Handler: a.fiber.Handler()}
	a.mu.Lock()
	a.zap = s
	a.mu.Unlock()
	return s.ListenAndServe()
}

// ListenHTTP serves plain HTTP — the optional EXTRA transport. Blocks.
func (a *App) ListenHTTP(addr string) error { return a.fiber.Listen(addr) }

// Serve runs ZAP (primary) and, when httpAddr != "", HTTP (extra) concurrently,
// returning the first listener error.
func (a *App) Serve(zapAddr, httpAddr string) error {
	errc := make(chan error, 2)
	go func() { errc <- a.ListenZAP(zapAddr) }()
	if httpAddr != "" {
		go func() { errc <- a.ListenHTTP(httpAddr) }()
	}
	return <-errc
}

// Shutdown stops both listeners.
func (a *App) Shutdown() error {
	a.mu.RLock()
	z := a.zap
	a.mu.RUnlock()
	if z != nil {
		_ = z.Close()
	}
	return a.fiber.Shutdown()
}
