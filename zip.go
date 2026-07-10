// Package zip is Hanzo's canonical Go web framework. Built on
// Fiber v3 / fasthttp. Sinatra-style API. ZAP-typed handlers.
// Multi-language extension support via HIP-0105.
//
// ONE framework, ZERO escape hatches. zip IS fast.
//
//	app := zip.New(zip.Config{Logger: luxlog.NewLogger("svc")})
//	app.Use(middleware.Recover(), middleware.RequestID())
//	app.Get("/health", func(c *zip.Ctx) error {
//	    return c.JSON(200, fiber.Map{"ok": true})
//	})
//	app.Listen(":9653", "http://:8080") // ZAP primary + HTTP extra, one verb
//
// Public surface — types/functions exposed at the package root:
//
//	type App, Config, Ctx, Handler
//	func New(Config) *App
//	func Get[I, O](app *App, path string, fn func(ctx, *I) (*O, error))
//	func Post[I, O](app *App, path string, fn func(ctx, *I) (*O, error))
//	...
//
// All other behavior lives in subpackages: `middleware`, `runtime`.
package zip

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v3"
	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip/internal/jsonenc"
	"github.com/zap-proto/zip/runtime"
)

// JSONVariant reports which JSON implementation zip is using in this
// build — "encoding/json/v2" when compiled with GOEXPERIMENT=jsonv2,
// "encoding/json" otherwise. Exposed for cmd/cloud startup logs and
// for tests that need to assert the variant. Per HIP-0106 the wire
// stack is "JSON only at edge, ZAP between services"; this constant
// tells operators which JSON impl is on the edge.
const JSONVariant = jsonenc.Variant

// Handler is zip's request handler signature. Returning an error causes
// Fiber's error chain to write a JSON response.
type Handler func(c *Ctx) error

// Config configures the zip App. Most fields pass through to Fiber's own
// Config; a few zip-specific knobs control runtime loading.
type Config struct {
	// Logger is the luxfi/log Logger zip uses internally. Required.
	// If nil, a default one is created via luxlog.NewLogger("zip").
	Logger luxlog.Logger

	// Loader is the HIP-0105 extension runtime loader. nil disables
	// app.Module() — only native handlers will work. The interface is
	// satisfied by *extruntime.Loader from hanzoai/base/plugins/extruntime;
	// zip does NOT take a hard dep on hanzoai/base.
	Loader runtime.Loader

	// AllowedRuntimes restricts which extension runtimes app.Module()
	// will accept (e.g. ["goja","wazero"] for hard multi-tenant safety).
	// nil = allow whatever the Loader has registered.
	AllowedRuntimes []string

	// ServerHeader is sent as the Server: response header (default "zip").
	// Set to "-" to suppress.
	ServerHeader string

	// BodyLimit is the maximum request body size (default 4 MiB).
	BodyLimit int

	// AppName forwards to fiber.Config.AppName.
	AppName string

	// DisableStartupMessage suppresses Fiber's startup banner.
	DisableStartupMessage bool

	// ErrorHandler is the catch-all error handler. Defaults to zip.errorHandler
	// which renders {error, code, status} JSON.
	ErrorHandler fiber.ErrorHandler

	// Concurrency caps the maximum number of concurrent connections the
	// server will accept. Default 0 means fasthttp's own default
	// (256*1024). Ops should cap this at the per-replica budget — see
	// `~/work/hanzo/hips/docs/SCALE_STANDARD.md`. With Hanzo's verified
	// 8 KiB/conn budget, 100_000 sits at ~800 MiB inside a 1 GiB pod.
	Concurrency int

	// ReadBufferSize is fasthttp's per-conn request-read buffer (default
	// 4 KiB). Raise only for header-heavy upstreams; raising it inflates
	// the per-conn memory budget and breaks the conn-memory regression
	// gate (see SCALE_STANDARD.md §8).
	ReadBufferSize int

	// WriteBufferSize is fasthttp's per-conn response-write buffer
	// (default 4 KiB). Raise only for streaming-heavy responses; same
	// budget caveat as ReadBufferSize.
	WriteBufferSize int

	// OpenAPI configures the auto-generated /.well-known/openapi.json
	// served when typed handlers are registered.
	OpenAPI OpenAPIConfig

	// MCP configures the Model Context Protocol tool surface auto-derived from
	// typed handlers (Get/Post[In,Out]). Enabled by default — it's free (the
	// same op registry that feeds OpenAPI), served over every transport. Set
	// MCP.Disabled to suppress.
	MCP MCPConfig
}

// App is the zip application. It wraps *fiber.App and exposes the zip
// handler signature alongside generic typed handlers.
type App struct {
	cfg         Config
	logger      luxlog.Logger
	loader      runtime.Loader
	fiber       *fiber.App
	ops         []*registeredOp
	closers     []func() error
	servers     []Server // the running transport listeners, set by Listen
	srvMu       sync.Mutex
	prepareOnce sync.Once // installs deferred routes (OpenAPI, MCP) exactly once

	// Route precedence is DATA (the pattern), not a PLACE (the call order).
	// Static route registrations buffer here instead of touching fiber, then
	// finalize() sorts them most-specific-first exactly once. See the
	// "specificity-based route precedence" block below.
	routesMu     sync.Mutex
	routeBuf     []bufferedRoute
	finalizeOnce sync.Once
	finalized    bool
}

// New constructs an App with the given config. Defaults are applied
// for any zero-valued field.
func New(cfg Config) *App {
	if cfg.Logger == nil {
		cfg.Logger = luxlog.New("module", "zip")
	}
	if cfg.BodyLimit == 0 {
		cfg.BodyLimit = 4 << 20
	}
	if cfg.ServerHeader == "" {
		cfg.ServerHeader = "zip"
	}

	fcfg := fiber.Config{
		AppName:   cfg.AppName,
		BodyLimit: cfg.BodyLimit,
		// Route every Fiber JSON path through zip's jsonenc package: this
		// covers c.JSON(), c.Bind().Body(), and the default error
		// handler when it serializes HTTPError. With GOEXPERIMENT=jsonv2
		// the underlying impl is encoding/json/v2; otherwise it falls
		// back to encoding/json. Same call site, different bytes-out.
		JSONEncoder: jsonenc.Marshal,
		JSONDecoder: jsonenc.Unmarshal,
		// Per-conn scale knobs from SCALE_STANDARD.md §6 — zero values
		// fall through to fasthttp defaults (256k concurrent / 4 KiB
		// read+write buffers).
		Concurrency:     cfg.Concurrency,
		ReadBufferSize:  cfg.ReadBufferSize,
		WriteBufferSize: cfg.WriteBufferSize,
	}
	if cfg.ServerHeader != "-" {
		fcfg.ServerHeader = cfg.ServerHeader
	}
	if cfg.ErrorHandler != nil {
		fcfg.ErrorHandler = cfg.ErrorHandler
	} else {
		fcfg.ErrorHandler = errorHandler
	}

	cfg.Logger.Info("zip new", "json_variant", jsonenc.Variant)

	return &App{
		cfg:    cfg,
		logger: cfg.Logger,
		loader: cfg.Loader,
		fiber:  fiber.New(fcfg),
	}
}

// Fiber returns the underlying *fiber.App. Use for one-off escape into
// Fiber-only APIs (rare). Prefer staying on the zip surface.
//
// Fiber() finalizes the app first: it flushes the specificity-sorted route
// set into fiber. Anything you then register straight on the returned
// *fiber.App follows fiber's native registration order AFTER the sorted set —
// raw users own that ordering, same as before.
func (a *App) Fiber() *fiber.App { a.finalize(); return a.fiber }

// Logger returns the App's logger.
func (a *App) Logger() luxlog.Logger { return a.logger }

// Shutdown gracefully stops both transports.
func (a *App) Shutdown() error {
	a.closeServers()
	_ = a.runClosers(context.Background())
	return a.fiber.Shutdown()
}

// ShutdownWithContext gracefully stops both transports bounded by ctx.
func (a *App) ShutdownWithContext(ctx context.Context) error {
	a.closeServers()
	_ = a.runClosers(ctx)
	return a.fiber.ShutdownWithContext(ctx)
}

// Use registers zip-style middleware. Each Handler runs in order; calling
// c.Next() (via c.Continue) chains to the next handler. Middleware is applied
// immediately in DECLARATION order — it is never sorted. Because routes buffer
// until finalize(), all Use middleware naturally flushes before any route.
func (a *App) Use(handlers ...Handler) Router {
	for _, h := range handlers {
		a.fiber.Use(toFiberHandler(a, h))
	}
	return &rootRouter{a}
}

// UseFiber lets callers register raw fiber.Handler middleware (for the
// fiber/v3/middleware/* packages). zip middleware is preferred.
func (a *App) UseFiber(handlers ...fiber.Handler) Router {
	args := make([]any, 0, len(handlers))
	for _, h := range handlers {
		args = append(args, h)
	}
	a.fiber.Use(args...)
	return &rootRouter{a}
}

// Get / Post / Put / Patch / Delete / Head / Options / All / Add register routes.
func (a *App) Get(path string, h Handler) Router    { return a.method("GET", path, h) }
func (a *App) Post(path string, h Handler) Router   { return a.method("POST", path, h) }
func (a *App) Put(path string, h Handler) Router    { return a.method("PUT", path, h) }
func (a *App) Patch(path string, h Handler) Router  { return a.method("PATCH", path, h) }
func (a *App) Delete(path string, h Handler) Router { return a.method("DELETE", path, h) }
func (a *App) Head(path string, h Handler) Router   { return a.method("HEAD", path, h) }
func (a *App) Options(path string, h Handler) Router {
	return a.method("OPTIONS", path, h)
}

// All registers a handler for any HTTP method. It buffers like the other
// route verbs; "*" is its method token for sorting and conflict detection.
func (a *App) All(path string, h Handler) Router {
	fh := toFiberHandler(a, h)
	a.bufferRoute("*", path, func() { a.fiber.All(path, fh) })
	return &rootRouter{a}
}

func (a *App) method(method, path string, h Handler) Router {
	fh := toFiberHandler(a, h)
	a.bufferRoute(method, path, func() { a.fiber.Add([]string{method}, path, fh) })
	return &rootRouter{a}
}

// Group creates a path-prefixed router group.
func (a *App) Group(prefix string, handlers ...Handler) Router {
	args := make([]any, 0, len(handlers))
	for _, h := range handlers {
		args = append(args, toFiberHandler(a, h))
	}
	g := a.fiber.Group(prefix, args...)
	return &routerAdapter{r: g, app: a}
}

// Route runs fn against a path-prefixed router group.
func (a *App) Route(prefix string, fn func(r Router)) Router {
	g := a.fiber.Group(prefix)
	r := &routerAdapter{r: g, app: a}
	fn(r)
	return r
}

// errors.As helper for HTTPError unwrapping in tests / external callers.
func asHTTPError(err error) (*HTTPError, bool) {
	var he *HTTPError
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}

// --- specificity-based route precedence (ServeMux-1.22 semantics) ---
//
// Fiber matches routes in REGISTRATION ORDER: the first registered pattern that
// matches a request wins. That braids route PRECEDENCE (a property of the
// pattern) with registration ORDER (a property of the call site) — which is why
// consumers grew "subsystem order-int" hacks purely to make specific routes
// (e.g. /v1/iam/keys) register before wildcards (/v1/iam/*). zip decomplects the
// two: App.Get/Post/… buffer their (method, pattern, handler) instead of
// touching fiber, and finalize() sorts the buffer MOST-SPECIFIC-FIRST before
// registering into fiber — so fiber's first-match then yields most-specific-
// wins, and the order you called Get/Post in no longer matters. This is exactly
// what Go 1.22 did to net/http.ServeMux. One way, no order-ints, no config knob.

// bufferedRoute is one deferred static registration. add performs the actual
// fiber registration (unchanged from the immediate path); method+pattern drive
// the specificity sort and the exact-duplicate check. App.All uses method "*".
type bufferedRoute struct {
	method  string
	pattern string
	add     func()
}

// bufferRoute defers a static route registration until finalize, where it is
// sorted by specificity. Registering after finalize (i.e. after Listen/Fiber())
// is a programming error — route tables are a startup-time concern.
func (a *App) bufferRoute(method, pattern string, add func()) {
	a.routesMu.Lock()
	defer a.routesMu.Unlock()
	if a.finalized {
		panic("zip: register routes before Listen")
	}
	a.routeBuf = append(a.routeBuf, bufferedRoute{method: method, pattern: pattern, add: add})
}

// finalize sorts every buffered route most-specific-first, rejects an exact
// (method, pattern) duplicate with a loud startup panic (never a silent
// shadow), registers the sorted set into fiber, then installs the deferred
// OpenAPI/MCP projections (which land AFTER the sorted set, exactly as before).
// Runs exactly once; triggered by Listen and Fiber().
func (a *App) finalize() {
	a.finalizeOnce.Do(func() {
		a.routesMu.Lock()
		routes := a.routeBuf
		a.routeBuf = nil
		a.finalized = true
		a.routesMu.Unlock()

		// Exact (method, pattern) duplicate → conflict. Checked against a set
		// (not sort adjacency) so equal-specificity siblings never mask it.
		seen := make(map[string]struct{}, len(routes))
		for _, r := range routes {
			key := r.method + " " + r.pattern
			if _, dup := seen[key]; dup {
				panic("zip: route conflict: " + r.method + " " + r.pattern + " registered more than once")
			}
			seen[key] = struct{}{}
		}

		sort.SliceStable(routes, func(i, j int) bool {
			return compareSpecificity(routes[i], routes[j]) < 0
		})
		for _, r := range routes {
			r.add()
		}
		a.prepare()
	})
}

// compareSpecificity orders two routes most-specific-first: it returns a
// negative number when a is more specific than b (a sorts first), positive when
// b is. It only returns 0 for identical (method, pattern), which finalize's set
// check has already rejected as a conflict.
func compareSpecificity(a, b bufferedRoute) int {
	as, bs := routeSegs(a.pattern), routeSegs(b.pattern)
	// 1. Segment by segment: at the first position whose KIND differs, the
	//    more-specific kind wins — static literal (0) < :param (1) < wildcard * (2).
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if ka, kb := segKind(as[i]), segKind(bs[i]); ka != kb {
			return ka - kb
		}
	}
	// 2. More static segments wins.
	if sa, sb := staticCount(as), staticCount(bs); sa != sb {
		return sb - sa
	}
	// 3. Deeper (more segments) beats shallower.
	if len(as) != len(bs) {
		return len(bs) - len(as)
	}
	// 4. Determinism tie-breaks: longer literal first, then method.
	if la, lb := literalLen(as), literalLen(bs); la != lb {
		return lb - la
	}
	if a.method != b.method {
		if a.method < b.method {
			return -1
		}
		return 1
	}
	return 0
}

// routeSegs splits a pattern into its non-empty path segments.
func routeSegs(pattern string) []string {
	raw := strings.Split(pattern, "/")
	segs := raw[:0]
	for _, s := range raw {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// segKind ranks one segment by specificity: static literal (0) is more specific
// than a :param (1), which is more specific than a * wildcard (2).
func segKind(seg string) int {
	switch {
	case seg == "":
		return 0
	case seg[0] == ':':
		return 1
	case seg[0] == '*':
		return 2
	default:
		return 0
	}
}

func staticCount(segs []string) int {
	n := 0
	for _, s := range segs {
		if segKind(s) == 0 {
			n++
		}
	}
	return n
}

func literalLen(segs []string) int {
	n := 0
	for _, s := range segs {
		if segKind(s) == 0 {
			n += len(s)
		}
	}
	return n
}

// rootRouter is the Router the App's own Get/Post/…/Use return. Unlike a Group
// (a prefix scope that registers straight onto fiber in declaration order),
// every route registered through it flows back through the App's specificity
// buffer — so a fluent `app.Get(…).Post(…)` chain sorts with everything else
// and registration order stays irrelevant. Group/Route still open prefix scopes.
type rootRouter struct{ app *App }

func (r *rootRouter) Use(h ...Handler) Router                { return r.app.Use(h...) }
func (r *rootRouter) Get(p string, h Handler) Router         { return r.app.Get(p, h) }
func (r *rootRouter) Post(p string, h Handler) Router        { return r.app.Post(p, h) }
func (r *rootRouter) Put(p string, h Handler) Router         { return r.app.Put(p, h) }
func (r *rootRouter) Patch(p string, h Handler) Router       { return r.app.Patch(p, h) }
func (r *rootRouter) Delete(p string, h Handler) Router      { return r.app.Delete(p, h) }
func (r *rootRouter) Head(p string, h Handler) Router        { return r.app.Head(p, h) }
func (r *rootRouter) Options(p string, h Handler) Router     { return r.app.Options(p, h) }
func (r *rootRouter) All(p string, h Handler) Router         { return r.app.All(p, h) }
func (r *rootRouter) Group(p string, h ...Handler) Router    { return r.app.Group(p, h...) }
func (r *rootRouter) Route(p string, fn func(Router)) Router { return r.app.Route(p, fn) }
func (r *rootRouter) Fiber() fiber.Router                    { return r.app.Fiber() }
