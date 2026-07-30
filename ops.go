package zip

import (
	"os"
	"strconv"
	"strings"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
)

// The ops surface — HIP-0119 §1 and §3, in one place.
//
// Every Hanzo service answers /healthz, /readyz and /metrics on a SECOND
// listener, never on the public one: a liveness probe must not queue behind
// public traffic, and a metrics endpoint on a public port is an information
// leak. Before this, the paths were named by HIP-0119 and implemented by
// nobody — OPS_PORT appeared in no Go file in the fleet — so conformance meant
// 116 repositories about to hand-roll three routes and a listener each.
//
// It is not a separate verb. [App.Listen] brings the ops listener up when the
// environment names one, which means:
//
//	standalone   the deployment sets OPS_PORT → the ops listener runs
//	under a host the host sets no OPS_PORT    → it does not
//
// and that is exactly the split HIP-0106 §1.3(f) states: a child does not own
// the ops listener, because the ops port belongs to the deployment and a
// child's private socket is not one of the deployment's listeners.

// The ops paths, per HIP-0119 §3. They are zip's control plane on the ops app,
// so they never appear in a [Declaration].
const (
	HealthPath  = "/healthz"
	ReadyPath   = "/readyz"
	MetricsPath = "/metrics"
)

// OpsPortEnv names the ops listener's port. HIP-0119 §5 spells it OPS_PORT;
// OpsAddr also accepts a full address in ZIP_OPS_ADDR for a deployment that
// needs to bind one interface.
const (
	OpsPortEnv = "OPS_PORT"
	OpsAddrEnv = "ZIP_OPS_ADDR"
)

// DefaultOpsPort is the port HIP-0119 §1 names, stated once so a deployment's
// manifest and a local run agree. It is NOT a fallback: see [OpsAddr].
const DefaultOpsPort = "9090"

// OpsAddr is the address this process's ops listener binds, or "" when this
// process does not own one:
//
//	$ZIP_OPS_ADDR   a full address, always wins
//	$OPS_PORT       a port, per HIP-0119 §5
//	otherwise       "" — this process owns no ops listener
//
// There is deliberately NO fallback. A default would mean every process that
// calls Listen tries to bind one port, and a plugin has more than one app in it
// (its edge app and its peer app) while a test process has many — so the
// SECOND listener in any of them dies on "address already in use", which is
// exactly the failure HIP-0106 §2 names. The ops port is deployment
// configuration (HIP-0119 §5) and a deployment states it; a host does not set
// it on a child, so a child correctly owns none.
func OpsAddr() string {
	if a := strings.TrimSpace(os.Getenv(OpsAddrEnv)); a != "" {
		return a
	}
	if p := strings.TrimSpace(os.Getenv(OpsPortEnv)); p != "" {
		if _, err := strconv.Atoi(p); err == nil {
			return ":" + p
		}
		return p
	}
	return ""
}

// Ops is this app's ops sibling: a second [App] serving /healthz, /readyz and
// /metrics and nothing else, so the public listener carries no ops surface and
// the ops listener carries no public routes.
//
// Memoized — one sibling per app, whoever asks — because two ops apps would
// report two readiness answers for one process.
func (a *App) Ops() *App {
	a.opsOnce.Do(func() {
		ops := New(Config{
			AppName:               a.cfg.AppName + "-ops",
			Logger:                a.logger,
			DisableStartupMessage: true,
			// The ops app has no typed ops, so it projects no document and needs
			// no MCP door; disabling them says so rather than relying on the
			// emptiness of the registry.
			OpenAPI: OpenAPIConfig{Disabled: true},
			MCP:     MCPConfig{Disabled: true},
		})
		// Liveness: the process is running and answering. It deliberately does
		// NOT consult a dependency — a liveness probe that fails when a database
		// blinks restarts a healthy process and turns one outage into two.
		ops.control(fiber.MethodGet, HealthPath, func(fc fiber.Ctx) error {
			return fc.SendString("ok")
		})
		// Readiness: this process's own listeners are bound. That is the whole of
		// what zip knows and can prove; a readiness answer a plugin needs to
		// widen (a store opened, a cache warmed) it widens in its own handler.
		ops.control(fiber.MethodGet, ReadyPath, func(fc fiber.Ctx) error {
			if a.listening.Load() == 0 {
				return Errorf(503, "not listening yet")
			}
			return fc.SendString("ready")
		})
		ops.control(fiber.MethodGet, MetricsPath, func(fc fiber.Ctx) error {
			fc.Set(fiber.HeaderContentType, "text/plain; version=0.0.4; charset=utf-8")
			return fc.SendString(a.metrics())
		})
		ops.sibling = true // a sibling has no siblings of its own
		a.ops = ops
	})
	return a.ops
}

// metrics is what zip actually knows about this process, in Prometheus text.
// Every value is read from a live structure — the op registry, the router, the
// plugin table, the process's own start — so none of it can be stale and none of
// it is a placeholder. A plugin's own metrics are its own to add.
func (a *App) metrics() string {
	var b strings.Builder
	b.WriteString("# HELP zip_up 1 when the app's listeners are bound.\n# TYPE zip_up gauge\nzip_up ")
	if a.listening.Load() > 0 {
		b.WriteString("1\n")
	} else {
		b.WriteString("0\n")
	}
	b.WriteString("# HELP zip_uptime_seconds Seconds since the app was constructed.\n")
	b.WriteString("# TYPE zip_uptime_seconds gauge\nzip_uptime_seconds ")
	b.WriteString(strconv.FormatFloat(time.Since(a.born).Seconds(), 'f', 3, 64))
	b.WriteString("\n# HELP zip_ops Registered typed operations.\n# TYPE zip_ops gauge\nzip_ops ")
	b.WriteString(strconv.Itoa(len(a.registry)))
	b.WriteString("\n# HELP zip_routes Registered routes.\n# TYPE zip_routes gauge\nzip_routes ")
	b.WriteString(strconv.Itoa(len(a.fiber.GetRoutes(false))))
	b.WriteString("\n")
	if st := a.Plugins(); len(st) > 0 {
		b.WriteString("# HELP zip_plugin_running 1 when a composed plugin has a live instance.\n")
		b.WriteString("# TYPE zip_plugin_running gauge\n")
		for _, s := range st {
			b.WriteString(`zip_plugin_running{name="`)
			b.WriteString(s.Name)
			b.WriteString(`"} `)
			if s.Running {
				b.WriteString("1\n")
			} else {
				b.WriteString("0\n")
			}
		}
	}
	return b.String()
}

// serveOps brings the ops listener up alongside the public one when this
// process owns an ops port. Called from Listen, so a plugin main ends with one
// verb and the environment decides whether there is a second listener.
//
// Only the PRIMARY app does this. An app's siblings (its ops app, its peer app)
// are the same process and report through the primary's surface.
func (a *App) serveOps() {
	if a.sibling || a.ops != nil {
		return // a sibling serves no siblings; and Listen may be called twice
	}
	addr := OpsAddr()
	if addr == "" {
		return
	}
	ops := a.Ops()
	go func() {
		if err := ops.Listen(addr); err != nil {
			a.logger.Error("zip ops listener stopped", "addr", addr, "err", err)
		}
	}()
}
