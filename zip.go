// Package zip is Hanzo's canonical Go web framework. Built on
// Fiber v3 / fasthttp. ZAP-typed handlers.
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
//	func Dial(addr string) (*Conn, error) / DialApp(name string) (*Conn, error)
//	func Call[I, O](ctx, *Conn, op string, in *I) (*O, error)
//	...
//
// All other behavior lives in subpackages: `middleware`, `js`.
package zip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
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
	// app.Module() — only native handlers will work. zip does NOT take a hard
	// dep on hanzoai/base: the consumer builds the loader and passes it in.
	// Note that [Loader] is satisfied structurally but NOT loosely — LoadDir
	// must return map[string][Module] exactly, so a backend with its own
	// Module type (e.g. hanzoai/base/plugins/extruntime) needs a thin adapter,
	// not a direct assignment. For JavaScript, import
	// [github.com/zap-proto/zip/js] — a separate import so its goja +
	// esbuild cost lands only on binaries that evaluate JS.
	Loader Loader

	// AllowedRuntimes restricts which extension runtimes app.Module()
	// will accept (e.g. ["goja","wazero"] for hard multi-tenant safety).
	// nil = allow whatever the Loader has registered.
	AllowedRuntimes []string

	// ServerHeader is sent as the Server: response header (default "zip").
	// Set to "-" to suppress.
	ServerHeader string

	// BodyLimit is the maximum request body size (default 4 MiB).
	BodyLimit int

	// AppName forwards to fiber.Config.AppName. It is also the plugin's ONE
	// name: the binary's name, its socket's stem ([SocketPath]), its
	// [Declaration].Name and its <org>-<app> IAM segment. No mapping table.
	AppName string

	// Eager says this app's work is NOT request-driven: it owns a listener, a
	// consumer or a background loop, so a host MUST start it rather than defer
	// it to the first request that reaches one of its routes.
	//
	// It is the ONE fact about an app that its router cannot show, which is why
	// it is stated here and travels in the [Declaration]. Everything else a host
	// needs to route is projected from the router itself.
	//
	// A host reads it as the ceiling on its own deferral choice
	// ([Plugin].Lazy): deferring an eager app is the mismatch §3.4 rejects.
	Eager bool

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

	// OpsAddr is where this app's ops listener binds — /healthz, /readyz and
	// /metrics on a socket of their own, so a liveness probe never queues behind
	// public traffic and a metrics endpoint is never on a public port. Empty
	// means this process owns no ops listener, which is what a child composed
	// into a host wants: the ops port belongs to the deployment, and a child's
	// private socket is not one of the deployment's listeners (HIP-0106 §1.3(f)).
	//
	// It is an address like every other address zip takes, and the address names
	// its own transport — so an ops listener a kubelet probes and Prometheus
	// scrapes is [DefaultOpsAddr], an HTTP one. A bare ":9090" would be ZAP
	// (DefaultScheme), which those two clients cannot speak.
	OpsAddr string

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
	cfg    Config
	logger luxlog.Logger
	loader Loader

	// The PROGRAM. entries is this app's ordered composition — middleware,
	// routes and other Apps included by reference — and prefix is the path this
	// app answers under when it is included somewhere ([App.Group] sets it).
	// There is no op-registry field: the registry is a PROJECTION over a walk of
	// this list (see [App.Registry]), which is what let one verb replace five.
	prefix  string
	entries []entry

	// The GENERATIONS. live is the sealed, immutable program currently serving,
	// swapped whole and read without a lock; draft is the throwaway build that
	// answers inspection before anything is live. buildMu serialises building —
	// never reading. See generation.go.
	live     atomic.Pointer[generation]
	draft    *generation
	draftAt  uint64
	draftErr error
	buildMu  sync.Mutex
	// control is zip's own projection routes. They belong to the BUILD, not to
	// the program — see [App.control].
	ctl []route

	// frozen: this definition has appeared in a built generation, so editing it
	// in place is refused. Monotonic, and propagated across the whole reachable
	// graph at install.
	frozen     atomic.Bool
	freezeSite callsite

	// wrap is the Middleware a scoped [App.With] installed on this App (see
	// wrapRouter.Group). nil for every ordinary App, and the common case pays
	// nothing for it.
	wrap Middleware

	servers []Server // the running transport listeners, set by Listen

	// authorizer, when set via Authorize, runs at every typed op's invoke seam
	// on the decoded In — the one place REST and MCP both funnel the value the
	// handler will act on. nil leaves every decoded request unauthorized.
	authorizer Authorizer

	srvMu sync.Mutex

	// Teardown lifecycle. hooks are drained LIFO by Shutdown after
	// in-flight requests finish; shuttingDown guards against re-running
	// them and against post-shutdown registration. hookMu guards both.
	hooks        []func(context.Context) error
	shuttingDown bool
	hookMu       sync.Mutex

	// Running plugins, by name, so Reload can find one after Load composed it.
	// Guarded by plugMu.
	plugins map[string]*plugin

	// The composed MCP surface of the plugins this app Load'ed: every tool
	// descriptor a plugin's build-time catalogue declared, and which plugin owns
	// each name. Written by load (under plugMu), read by installMCP and by a
	// tools/call. It is what lets a host expose 112 lazy children's tools without
	// running any of them — see [Plugin.Tools].
	pluginTools []mcpTool
	toolOwners  map[string]*plugin
	// open is the one plugin whose catalogue is INCOMPLETE by construction — the
	// tenant half of the door, asked per caller instead of embedded. See
	// [Plugin.Open]. At most one, so a name no catalogue claims has one owner.
	open   *plugin
	plugMu sync.Mutex

	// mcpList is the whole tools/list array, rendered by installMCP and re-rendered
	// by any later load: own ops ++ every plugin catalogue, sorted by name. Serving
	// bytes is what makes the most-called MCP method free. Atomic because a Load
	// after Listen re-renders it while requests are reading it — nil until the door
	// is installed, which is also how installTools knows whether to re-render.
	mcpList atomic.Pointer[json.RawMessage]

	// mcpNames is the set of names inside mcpList, stored with it so the
	// per-caller half can tell whether a tenant tool would shadow a projected one
	// without re-parsing the array on every request.
	mcpNames atomic.Pointer[map[string]bool]

	// The ops sibling (/healthz, /readyz, /metrics) and the listener count its
	// readiness reports. born is the process's own clock for zip_uptime_seconds.
	ops      *App // the /healthz + /readyz + /metrics sibling; see App.Ops
	opsOnce  sync.Once
	peer     *App // the peer-op sibling served on the canonical socket; see App.Peer
	peerOnce sync.Once
	// sibling marks an App that IS one of the two above, so it does not grow
	// siblings of its own and does not try to bind the process's ops port.
	sibling   bool
	listening atomic.Int32
	born      time.Time

	// controls is zip's OWN routes, keyed "METHOD path" — the openapi document,
	// the docs page, the MCP door, the op plane, this app's declaration. Written
	// only by App.control, read only by Declaration, so a control route cannot
	// be added without being excluded from every plugin's declaration.
	controls map[string]bool

	prepareOnce sync.Once // installs deferred routes (OpenAPI, MCP) exactly once
	// prepared reports whether that has happened, which sync.Once cannot be
	// asked. Graft needs the answer: the document, the tool list and the
	// call plane are rendered ONCE from the registry, so an op that arrives
	// afterwards would serve and never describe.
	prepared atomic.Bool
}

// New constructs an App with the given config. Defaults are applied
// for any zero-valued field.
func New(cfg Config) *App {
	a := newApp(cfg)
	a.logger.Info("zip new", "json_variant", jsonenc.Variant)
	return a
}

// newApp is New without the line in the log. [App.Group] builds an App per
// scope, and a group is a lexical construct rather than a deployment, so it
// does not announce itself.
//
// No router is constructed here. A router is a BUILD of the program, and the
// program is empty — materialising one now would mean materialising one per
// group, and rebuilding every one of them the moment anything is appended.
func newApp(cfg Config) *App {
	if cfg.Logger == nil {
		cfg.Logger = luxlog.New("module", "zip")
	}
	if cfg.BodyLimit == 0 {
		cfg.BodyLimit = 4 << 20
	}
	if cfg.ServerHeader == "" {
		cfg.ServerHeader = "zip"
	}
	return &App{
		cfg:    cfg,
		logger: cfg.Logger,
		loader: cfg.Loader,
		born:   time.Now(),
	}
}

// jsonMarshal / jsonUnmarshal route every fiber JSON path through zip's jsonenc
// package: c.JSON(), c.Bind().Body(), and the default error handler when it
// serialises an HTTPError. With GOEXPERIMENT=jsonv2 the underlying impl is
// encoding/json/v2; otherwise encoding/json. Same call site, different
// bytes-out. Named here so [App.fiberConfig] is the one place that says it.
var (
	jsonMarshal   = jsonenc.Marshal
	jsonUnmarshal = jsonenc.Unmarshal
)

// Fiber returns the underlying *fiber.App, materialising the program if it has
// changed since the last build. Use for one-off escape into Fiber-only APIs
// (rare). Prefer staying on the zip surface.
//
// It does NOT seal: inspecting the router is not executing it, and a test or a
// codegen step that looks must not turn the next legitimate Use into a panic.
func (a *App) Fiber() *fiber.App { return a.router() }

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

// With returns a Router whose subsequent leaf registrations (Get/Post/…/All)
// have mw wrapped around the handler at registration time — pure
// composition (RateLimit(CSRF(handler))). It does NOT touch the global Use
// stack and does NOT route through c.Next(); it is the per-route counterpart to
// Use. Routes registered on the returned Router still obey specificity
// precedence exactly like any other route.
//
//	app.With(RateLimit, CSRF).Post("/v1/keys", mintKey)
func (a *App) With(mw ...Middleware) Router {
	return &wrapRouter{inner: a, wrap: Chain(mw...)}
}

// Get / Post / Put / Patch / Delete / Head / Options / All register routes.
// Chains are in wrapping order: middleware first, the final handler last.
//
// These still take ...Handler and always will. A bare closure written inline is
// a *Handler by conversion, so nothing about route registration changes when
// composition widens — [zip.H] is needed only at [App.Use].
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
	return a.method(methodAll, path, handlers)
}

func (a *App) method(method, path string, handlers []Handler) Router {
	// A scoped [App.With] lives on the App itself, so a leaf registered on this
	// scope at any time is wrapped — including one registered after the scope was
	// handed out. A decorator could only wrap what passed through it.
	if len(handlers) == 0 {
		panic("zip: route registered with no handler")
	}
	for i, h := range handlers {
		// toFiberHandler wraps a nil Handler into a NON-nil closure, so fiber's
		// own nil check passes and the nil surfaces as a segfault on a serve
		// goroutine no recover middleware can reach. Boot, not request time.
		if h == nil {
			panic(fmt.Sprintf("zip: %s %s: handler %d is nil", method, normPath(path), i))
		}
	}
	if a.wrap != nil {
		handlers = append([]Handler(nil), handlers...)
		handlers[len(handlers)-1] = a.wrap(handlers[len(handlers)-1])
	} else {
		handlers = append([]Handler(nil), handlers...)
	}
	a.addRoute(here(2), route{method: method, path: normPath(path), chain: handlers})
	return a
}

// OpScope makes the App itself a place a typed op can be declared. The prefix
// is not reported here: a group's prefix is a property of WHERE the group is
// included, and one definition may be included in two places, so the absolute
// path is computed by the walk and never baked into the op.
func (a *App) OpScope() OpScope { return OpScope{App: a, Middleware: a.wrap} }

// errors.As helper for HTTPError unwrapping in tests / external callers.
func asHTTPError(err error) (*HTTPError, bool) {
	var he *HTTPError
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}
