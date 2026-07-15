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
	"sync"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3"

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
	cfg     Config
	logger  luxlog.Logger
	loader  runtime.Loader
	fiber   *fiber.App
	ops     []*registeredOp
	servers []Server // the running transport listeners, set by Listen
	srvMu   sync.Mutex

	// Teardown lifecycle. hooks are drained LIFO by Shutdown after
	// in-flight requests finish; shuttingDown guards against re-running
	// them and against post-shutdown registration. hookMu guards both.
	hooks        []func(context.Context) error
	shuttingDown bool
	hookMu       sync.Mutex

	prepareOnce sync.Once // installs deferred routes (OpenAPI, MCP) exactly once
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
func (a *App) Fiber() *fiber.App { return a.fiber }

// Logger returns the App's logger.
func (a *App) Logger() luxlog.Logger { return a.logger }

// Shutdown gracefully stops every transport, then runs teardown hooks.
// The process is ending, so hooks receive context.Background() — no
// cancellation or deadline. Use ShutdownWithContext to bound teardown.
// Idempotent: a second call is a no-op and hooks run at most once.
func (a *App) Shutdown() error {
	return a.shutdown(context.Background())
}

// ShutdownWithContext is Shutdown bounded by ctx: ctx bounds the in-flight
// drain and is passed to every teardown hook (values and deadline).
// Shares Shutdown's once-guard, so mixing the two still runs hooks once.
func (a *App) ShutdownWithContext(ctx context.Context) error {
	return a.shutdown(ctx)
}

// Use registers zip-style middleware. Each Handler runs in order; calling
// c.Next() (via c.Continue) chains to the next handler.
func (a *App) Use(handlers ...Handler) Router {
	for _, h := range handlers {
		a.fiber.Use(toFiberHandler(a, h))
	}
	return &routerAdapter{r: a.fiber, app: a}
}

// With returns a Router whose subsequent leaf registrations (Get/Post/…/All)
// have mw wrapped around the handler at registration time — chi's idiom, pure
// composition (RateLimit(CSRF(handler))). It does NOT touch the global Use
// stack and does NOT route through c.Next(); it is the per-route counterpart to
// Use. Routes registered on the returned Router still obey specificity
// precedence exactly like any other route.
//
//	app.With(RateLimit, CSRF).Post("/v1/keys", mintKey)
func (a *App) With(mw ...Middleware) Router {
	return &wrapRouter{
		inner: &routerAdapter{r: a.fiber, app: a},
		wrap:  Chain(mw...),
	}
}

// Get / Post / Put / Patch / Delete / Head / Options / All register routes.
// Chains are gin/express order: middleware first, the final handler last.
func (a *App) Get(path string, handlers ...Handler) Router  { return a.method("GET", path, handlers) }
func (a *App) Post(path string, handlers ...Handler) Router { return a.method("POST", path, handlers) }
func (a *App) Put(path string, handlers ...Handler) Router  { return a.method("PUT", path, handlers) }
func (a *App) Patch(path string, handlers ...Handler) Router {
	return a.method("PATCH", path, handlers)
}
func (a *App) Delete(path string, handlers ...Handler) Router {
	return a.method("DELETE", path, handlers)
}
func (a *App) Head(path string, handlers ...Handler) Router { return a.method("HEAD", path, handlers) }
func (a *App) Options(path string, handlers ...Handler) Router {
	return a.method("OPTIONS", path, handlers)
}

// All registers a handler for any HTTP method.
func (a *App) All(path string, handlers ...Handler) Router {
	h, mw := splitChain(a, handlers)
	a.fiber.All(path, h, mw...)
	return &routerAdapter{r: a.fiber, app: a}
}

func (a *App) method(method, path string, handlers []Handler) Router {
	h, mw := splitChain(a, handlers)
	a.fiber.Add([]string{method}, path, h, mw...)
	return &routerAdapter{r: a.fiber, app: a}
}

// Group creates a path-prefixed router group. The returned Router is the one
// way to register nested routes under a prefix — register leaves and further
// Groups directly on it; middleware scoped to the group goes on via its Use.
func (a *App) Group(prefix string, handlers ...Handler) Router {
	args := make([]any, 0, len(handlers))
	for _, h := range handlers {
		args = append(args, toFiberHandler(a, h))
	}
	g := a.fiber.Group(prefix, args...)
	return &routerAdapter{r: g, app: a}
}

// errors.As helper for HTTPError unwrapping in tests / external callers.
func asHTTPError(err error) (*HTTPError, bool) {
	var he *HTTPError
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}
