package zip

import (
	"strings"

	"github.com/luxfi/metric"
	fiber "github.com/zap-proto/fiber/v3"
)

// The ops surface — HIP-0119 §1 and §3, in one place.
//
// Every Hanzo service answers /healthz, /readyz and /metrics on a SECOND
// listener, never on the public one: a liveness probe must not queue behind
// public traffic, and a metrics endpoint on a public port is an information
// leak. Before this, the paths were named by HIP-0119 and implemented by
// nobody, so conformance meant 116 repositories about to hand-roll three routes
// and a listener each.
//
// It is not a separate verb. [App.Listen] brings the ops listener up when
// [Config.OpsAddr] names one, which means:
//
//	standalone   the deployment names an ops address → the ops listener runs
//	under a host the host names none for the child   → it does not
//
// and that is exactly the split HIP-0106 §1.3(f) states: a child does not own
// the ops listener, because the ops port belongs to the deployment and a
// child's private socket is not one of the deployment's listeners.
//
// The address is an address, on the same footing and in the same grammar as the
// ones [App.Listen] takes, so a binary states all of its listeners one way:
//
//	svc serve --zap :9653 --http http://:8000 --ops http://:9090

// The ops paths, per HIP-0119 §3. They are zip's control plane on the ops app,
// so they never appear in a [Declaration].
const (
	HealthPath  = "/healthz"
	ReadyPath   = "/readyz"
	MetricsPath = "/metrics"
)

// DefaultOpsAddr is the address HIP-0119 §1 names, stated once so a
// deployment's manifest, a binary's flag default and a local run agree. It is
// the value, not a fallback: [Config.OpsAddr] left empty binds nothing.
//
// A default that applied itself would mean every process calling Listen tries
// to bind this one port, and a plugin has more than one app in it (its edge app
// and its peer app) while a test process has many — so the SECOND listener in
// any of them dies on "address already in use", which is exactly the failure
// HIP-0106 §2 names. The ops address is deployment configuration (HIP-0119 §5)
// and a deployment states it; a host states none for a child, so a child
// correctly owns none.
//
// It carries the http:// scheme because the two clients this surface exists for
// — a kubelet httpGet probe and a Prometheus scrape — speak HTTP and nothing
// else. A bare ":9090" is ZAP ([DefaultScheme]), and a probe against a ZAP
// socket is read as a frame: "GET " arrives as frame size 1195725856.
const DefaultOpsAddr = "http://:9090"

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
		// A sibling reports into the PRIMARY's instruments, because the two are
		// one process and a scrape of that process must show everything it
		// answered. A sibling with a registry of its own would measure faithfully
		// into a registry nothing gathers — instrumented and invisible, which is
		// the failure this whole file exists to end.
		ops.telemetry = a.telemetry
		a.ops = ops
	})
	return a.ops
}

// metrics is what this process measured, in Prometheus text: zip's own state
// and the RED numbers for every request the app answered.
//
// It renders the app's registry and nothing else. It used to assemble the text
// by hand from live structures, which was honest as far as it went but meant
// the scrape surface and the exported batch were two renderings of two
// different sources — so a number could appear on /metrics and never reach
// o11y, or disagree with the copy that did. One registry, gathered one way,
// rendered by whoever is asking. A plugin's own metrics are its own to add.
func (a *App) metrics() string {
	families, err := a.gather()
	if err != nil {
		a.logger.Error("zip metrics gather failed", "err", err)
		return ""
	}
	var b strings.Builder
	if err := metric.EncodeText(&b, families); err != nil {
		a.logger.Error("zip metrics encode failed", "err", err)
		return ""
	}
	return b.String()
}

// serveOps brings the ops listener up alongside the public one when this
// process owns an ops address. Called from Listen, so a plugin main ends with
// one verb and the app's own declaration decides whether there is a second
// listener.
//
// Only the PRIMARY app does this. An app's siblings (its ops app, its peer app)
// are the same process and report through the primary's surface.
func (a *App) serveOps() {
	if a.sibling || a.ops != nil {
		return // a sibling serves no siblings; and Listen may be called twice
	}
	addr := strings.TrimSpace(a.cfg.OpsAddr)
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
