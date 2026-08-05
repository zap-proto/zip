package zip

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/luxfi/metric"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip/internal/o11y"
)

// Telemetry is a property of running, not a feature an app switches on.
//
// Every program built on zip reports the same three things about every request
// it answers — a log line, the RED numbers (rate, errors, duration), and the
// trace context that ties one hop to the next — and it reports them because it
// is serving, not because it called anything. There is no Use to remember, no
// middleware to order correctly, and no way for one of 125 plugins to be the
// one that forgot: the report is emitted by the same [App.materialise] that
// builds the router, so a program that serves is a program that reports.
//
// This is the whole reason the concern sits here rather than in each app. When
// it was per-app it was per-app CORRECT: one binary logged the caller and
// another did not, one measured latency and another measured it after the
// response had been written, and the one that mattered during an incident was
// whichever had been copied last. A fleet's telemetry is only worth reading if
// every member reports the same shape, and the only way to guarantee one shape
// is to have one implementation.
//
// What an app still owns is its own logger — [Config.Logger] is a
// luxlog.Logger and stays one. zip writes the request line through it, so an
// app's fields, level and destination are the app's; the LINE is the
// framework's.
//
// Which is also the answer to "why is only the request line in the store": the
// request line is the only line zip writes, so it is the only one zip can ship.
// Everything a handler logs goes where the app's logger points it, untouched.
// Taking every line off the box is one decision about logging in general, and it
// belongs to luxfi/log — the thing that already sees every line, for every
// program, whether or not that program serves requests. Made here it would be
// zip deciding for 125 programs that a log line leaves the machine.

// The one identifier a request carries between services, and the header it
// travels in.
//
// It is W3C trace context, verbatim, because the value's job is to be
// understood by something we did not write: a browser's fetch, an ingress, a
// client SDK. A private header would be a value only our own processes could
// read, which defeats the purpose of carrying it at all.
//
//	traceparent: 00-<32 hex trace>-<16 hex span>-<2 hex flags>
//
// zip reads it when it arrives and mints it when it does not, so a trace is
// continuous across every hop that is a zip app and begins at the first one
// that is.
const HeaderTrace = "traceparent"

// traceVersion is the only trace-context version this parses. The spec says a
// receiver must accept future versions by reading the fields it knows, but a
// version it does not know may reorder them — so an unknown version is treated
// as absent and a fresh trace begins, which is the safe direction: a broken
// link is visible, a misparsed one is not.
const traceVersion = "00"

// Telemetry is where this app's three reports go. Every field may stay empty
// forever, and an empty field means that signal is not exported.
//
// THE ADDRESS IS THE SWITCH, and it is the switch on both ends: the collector
// states exactly this rule for receiving — an empty address means that signal is
// not received in this process — so one sentence describes the producer and the
// consumer, and there is no enable flag on either side that can disagree with an
// address. A program with nothing stated still logs and still counts; it simply
// has nowhere to send, which costs it no goroutine, no connection and no port.
//
// A field is read first, and the matching variable in the environment second, so
// a deployment that states an address in its manifest and a process that
// inherits one from its host agree on which wins — the explicit one:
//
//	Spans     O11Y_SPANS_ADDR
//	Logs      O11Y_LOGS_ADDR
//	Metrics   O11Y_METRICS_ADDR
//
// Three names rather than one base address with the three conventional ports
// derived from it (spans 4317, logs 4318, metrics 4319). The collector binds one
// listener per signal at one address per signal, refuses two signals that name
// the same address, and receives whichever signals have one — so deriving two
// addresses from a third would assert an adjacency it does not promise, and
// would make "logs are off, spans are on" unsayable. Sibling names also state
// which port belongs to which signal at the place a deployment sets it, and a
// batch sent to the wrong ear is dropped by a receiver that decodes one message
// type, silently on both sides.
//
// What is reported is not configurable at all, because a fleet whose members
// report different shapes cannot be read as a fleet.
type Telemetry struct {
	// Spans is where this app's server spans go, as host:port.
	Spans string

	// Logs is where this app's request line goes, as host:port.
	//
	// Only zip's OWN request line ships here — the same line, with the same
	// fields, that the app's logger already writes. An app's other logging is
	// its own: its luxlog.Logger writes wherever the app pointed it and is
	// never intercepted, teed or rewritten by the framework. Shipping a
	// program's every log line off the box is one decision for the logger to
	// make, in luxfi/log, once, for everything that logs; taking it here would
	// mean zip quietly making it for 125 programs that never asked.
	Logs string

	// Metrics is where the numbers go, as host:port.
	Metrics string

	// Off stops this app reporting at all: no line, no numbers, no spans, no
	// trace context. It is for the app that must not emit — a test double, a
	// one-shot command — and it is the only switch of its kind, so "is this app
	// instrumented" has one answer and one place to read it.
	Off bool
}

// The environment's spelling of the three addresses. A deployment sets these;
// they are the names cloud's manifest already uses for the metric ear, extended
// to its two siblings.
const (
	envSpans   = "O11Y_SPANS_ADDR"
	envLogs    = "O11Y_LOGS_ADDR"
	envMetrics = "O11Y_METRICS_ADDR"
)

// addresses is where one app reports, resolved once when the app is built.
//
// Once, because there is nothing left to re-decide: a field is fixed at
// construction and the environment of a running process does not change. The
// three are held together in one value so "does this app export anything" is one
// question about one thing.
type addresses struct{ spans, logs, metrics string }

// resolve reads the three addresses: what the program stated, else what its
// environment states, else nothing.
func resolve(cfg Config) addresses {
	pick := func(stated, env string) string {
		if v := strings.TrimSpace(stated); v != "" {
			return v
		}
		return strings.TrimSpace(os.Getenv(env))
	}
	t := cfg.Telemetry
	return addresses{
		spans:   pick(t.Spans, envSpans),
		logs:    pick(t.Logs, envLogs),
		metrics: pick(t.Metrics, envMetrics),
	}
}

// any reports whether this app has anywhere at all to report to. It is what
// makes "off" cost nothing: with no address there is no exporter, so no node
// starts, no port is taken and no connection is attempted.
func (a addresses) any() bool { return a.spans != "" || a.logs != "" || a.metrics != "" }

// telemetry is one app's instruments. Built once in [newApp], read from every
// serving goroutine, never rebuilt: a generation swap re-installs the handler
// but must not reset the counters, or every composition change would look like
// a restart to whatever is reading the rate.
type telemetry struct {
	off bool

	// The registry is this app's, not a global one. Two apps in one process —
	// and a plugin host has many — would otherwise register the same instrument
	// twice and read each other's traffic as their own.
	registry metric.Registry

	// The RED instruments. Rate and errors are the same counter read two ways:
	// the total is the rate, and the status label is the errors. Keeping them as
	// one instrument is what makes "what fraction of requests failed" a division
	// rather than a join between two metrics that can disagree.
	requests metric.CounterVec
	duration metric.HistogramVec

	// zip's own state, as real instruments rather than text assembled by hand,
	// so [App.metrics] has one renderer over one registry instead of a string
	// builder that has to agree with a gatherer.
	up      metric.Gauge
	uptime  metric.Gauge
	ops     metric.Gauge
	routes  metric.Gauge
	plugins metric.GaugeVec
	dropped metric.Counter

	// Where this app reports, resolved once when it was built.
	to addresses

	// ship says this app has somewhere to send spans and lines, so the boundary
	// should collect them. False when neither address is stated, and then the
	// boundary keeps nothing: a buffer nobody will drain is a memory leak with a
	// good reason attached.
	//
	// It is decided WITH THE APP rather than when the export goroutine starts,
	// and that is not a micro-optimisation: Serve returns as soon as the serving
	// goroutine is launched, so a request answered immediately afterwards would
	// race the goroutine and its span would be silently dropped. Whether an app
	// exports is a property of its configuration, which is knowable here.
	ship bool

	// What the request boundary has produced and the exporter has not yet
	// shipped. A request APPENDS and returns; the socket is touched by the
	// export ticker on its own goroutine.
	//
	// This is the whole reason a buffer exists at all. Writing a span from the
	// handler would put the collector's latency — and its outages — inside the
	// request the span describes, which is the one place telemetry must never
	// be. See [telemetry.keep].
	pending sync.Mutex
	spans   []o11y.Span
	records []o11y.LogRecord
}

// pendingLimit caps what one export interval may accumulate.
//
// A collector that is down or slow must not turn into a memory leak in every
// service that reports to it, so past this point the OLDEST entries are dropped
// and the newest kept. Newest-wins because an operator reading after an incident
// wants the requests nearest the failure, and because the alternative — refusing
// new entries once full — freezes the buffer at the moment trouble started and
// hides everything that followed. The drop is counted, so a gap is visible as a
// number rather than inferred from silence.
const pendingLimit = 4096

// The labels the RED instruments carry, stated once so the counter and the
// histogram cannot drift apart.
//
// The route is the registered TEMPLATE ("/v1/orders/:id"), never the path that
// arrived, because a label whose values are unbounded is not a label — a metric
// keyed on "/v1/orders/8a3f…" grows one time series per order and takes the
// store down with it. The org is deliberately NOT here for the same reason, and
// it is on the log line and the span instead, where high cardinality is what
// those surfaces are for.
var (
	requestLabels  = []string{"method", "route", "status"}
	durationLabels = []string{"method", "route"}
)

func newTelemetry(cfg Config) *telemetry {
	t := &telemetry{off: cfg.Telemetry.Off}
	if t.off {
		return t
	}
	// An app collects spans and lines when it has somewhere to send them, and
	// only then. The numbers are different: they are always measured, because
	// /metrics serves them off this registry whether or not anything is pushed.
	t.to = resolve(cfg)
	t.ship = t.to.spans != "" || t.to.logs != ""
	t.registry = metric.NewRegistry()
	t.requests = t.registry.NewCounterVec(
		"http_requests_total",
		"Requests answered, by method, matched route and status.",
		requestLabels,
	)
	t.duration = t.registry.NewHistogramVec(
		"http_request_duration_seconds",
		"Time to answer a request, in seconds.",
		durationLabels,
		metric.DefBuckets,
	)
	t.up = t.registry.NewGauge("zip_up", "1 when the app's listeners are bound.")
	t.uptime = t.registry.NewGauge("zip_uptime_seconds", "Seconds since the app was constructed.")
	t.ops = t.registry.NewGauge("zip_ops", "Registered typed operations.")
	t.routes = t.registry.NewGauge("zip_routes", "Registered routes.")
	t.plugins = t.registry.NewGaugeVec(
		"zip_plugin_running",
		"1 when a composed plugin has a live instance.",
		[]string{"name"},
	)
	t.dropped = t.registry.NewCounter(
		"zip_telemetry_dropped_total",
		"Spans and log records discarded because the collector could not keep up.",
	)
	return t
}

// keep holds one request's span and line until the exporter drains them.
//
// Both are appended under one lock and both are subject to one limit, because
// they describe the same request: dropping the span while keeping the line would
// leave a log record pointing at a trace that has no span in it, which reads as
// a broken trace rather than as a dropped one.
func (t *telemetry) keep(s o11y.Span, r o11y.LogRecord) {
	t.pending.Lock()
	defer t.pending.Unlock()
	if len(t.spans) >= pendingLimit {
		// Drop the oldest, keep the newest. Counted so the gap is a number an
		// operator can see rather than an absence they have to notice.
		t.spans = append(t.spans[:0], t.spans[1:]...)
		t.records = append(t.records[:0], t.records[1:]...)
		t.dropped.Inc()
	}
	t.spans = append(t.spans, s)
	t.records = append(t.records, r)
}

// drain takes everything pending and hands the buffers back empty. The caller
// ships outside the lock, so a slow collector never blocks a request that is
// only trying to append.
func (t *telemetry) drain() ([]o11y.Span, []o11y.LogRecord) {
	t.pending.Lock()
	defer t.pending.Unlock()
	s, r := t.spans, t.records
	t.spans, t.records = nil, nil
	return s, r
}

// report is the request boundary: the one place a zip program says what it did.
//
// It is installed as the OUTERMOST router middleware, which is what makes it
// complete rather than nearly complete. Composed as a route's own chain it
// would see only requests that matched a route, and the requests worth hearing
// about during an incident are disproportionately the ones that matched
// nothing — a client calling a path that was renamed, a probe pointed at the
// wrong port. Those are 404s, and a 404 that is invisible is an outage nobody
// can attribute.
func (a *App) report() Handler {
	t := a.telemetry
	return func(c *Ctx) error {
		// The ops surface is not traffic. A liveness probe every second and a
		// scrape every fifteen would otherwise dominate both the rate and the
		// duration histogram, and make the numbers describe the monitoring rather
		// than the service.
		if isOps(c.fc.Path()) {
			return c.Continue()
		}

		// Every string kept past this handler is CLONED off the request.
		// fasthttp hands out views over a connection buffer it reuses for the
		// next request on that connection, so a value retained without copying
		// is read back later as whatever arrived afterwards. It surfaces as a
		// log line or a span whose path belongs to a different request — the
		// kind of corruption that is invisible in a test and pervasive under
		// load, because it needs concurrency on one connection to show up.
		method := strings.Clone(c.fc.Method())
		path := strings.Clone(c.fc.Path())

		tr := traceOf(c)
		start := time.Now()

		// The request-scoped logger carries the request's identity, so a line
		// written by a handler joins the line written here without the handler
		// naming any of it. This is what middleware.Logger used to install; it is
		// installed for every app now, in the one place that cannot be skipped.
		c.SetLog(describe(c.Log(), c, tr, method, path))

		err := c.Continue()

		dur := time.Since(start)
		status := outcome(c, err)
		route := routeOf(c, err)

		t.observe(method, route, status, dur)
		emit(c.Log(), status, dur, err)
		// The boundary IS the span. There is no tracer to start and stop, because
		// everything a server span carries — when it began, how long it took, what
		// it answered, who asked — is what this handler already measured. A tracer
		// API here would be a second way to describe one request.
		if t.exports() {
			t.keep(span(tr, method, route, path, status, dur, start, err, c),
				record(tr, method, route, path, status, dur, err, c))
		}
		return err
	}
}

// facts are the attributes a span and a log record both carry, built once so
// the two cannot describe one request differently.
//
// The path is here, at full cardinality, and that is correct: an attribute is
// not a metric label. A trace store is built to hold one row per request and is
// searched by exactly this — "show me the request for order 8a3f" — where the
// same value in a metric label would create a time series per order.
//
// The KEYS are the collector's vocabulary, not a spelling of our own. It reads
// a span's path column out of the first of url.path, http.target, http.route,
// path that a batch carries, and reads a log record's path column out of the
// same four — one vocabulary across both signals, so a query written against
// traces answers over logs too. A key it does not know is stored and searchable
// but fills no column, which is how a report can look complete and leave the
// column an operator actually searches empty.
func facts(c *Ctx, method, route, path string, status int) map[string]any {
	f := map[string]any{
		"http.request.method":       method,
		"url.path":                  path,
		"http.response.status_code": status,
	}
	if route != "" {
		f["http.route"] = route
	}
	for k, v := range map[string]string{
		"hanzo.org":      c.fc.Get(HeaderOrg),
		"hanzo.user":     c.fc.Get(HeaderUser),
		"hanzo.request":  c.fc.Get(HeaderRequestID),
		"client.address": CallerOf(c.Forward()).IP,
	} {
		if v != "" {
			f[k] = strings.Clone(v)
		}
	}
	return f
}

// span renders this request as the server span it is.
//
// The NAME is the route template, never the path, for the reason a metric label
// is bounded: a trace store groups by operation name to answer "how is
// /v1/orders/:id doing", and a name per order id makes that question
// unanswerable. The path is an attribute, where the cardinality belongs.
func span(tr trace, method, route, path string, status int, dur time.Duration, start time.Time, err error, c *Ctx) o11y.Span {
	name := route
	if name == "" {
		// Nothing matched, so there is no operation to name. The method alone is
		// bounded and honest — the path it was looking for rides as an attribute.
		name = method
	}
	s := o11y.Span{
		TraceID:      tr.trace,
		SpanID:       tr.span,
		ParentSpanID: tr.parent,
		Name:         name,
		// This app answered a request someone else sent, which is what "server"
		// means. The collector lowercases and strips "span_kind_", so the plain
		// spelling is the one to send.
		Kind:        "server",
		StartUnixNs: start.UnixNano(),
		EndUnixNs:   start.Add(dur).UnixNano(),
		Attributes:  facts(c, method, route, path, status),
		// Lowercase because that is what is stored: the collector lowercases the
		// value and strips a "status_code_" prefix, so sending what a query will
		// match saves everyone one translation.
		StatusCode: "ok",
	}
	// A span is in error when THIS service failed. A refusal is a correct answer
	// to a bad request, and marking it an error makes every trace view that
	// filters on errors mostly noise.
	if status >= 500 {
		s.StatusCode = "error"
		if err != nil {
			s.StatusMsg = err.Error()
		}
	}
	return s
}

// record renders the same request as the log line that ships.
//
// It is the line [emit] writes, in the shape the collector decodes: same
// request, same values, same one line for it, so the stream an operator greps
// on the box and the stream they query in the store cannot tell different
// stories. It carries the trace and span ids too, which is the whole reason a
// log address is worth setting: they are what lets a store put this line INSIDE
// the span above it rather than beside it.
func record(tr trace, method, route, path string, status int, dur time.Duration, err error, c *Ctx) o11y.LogRecord {
	f := facts(c, method, route, path, status)
	f["duration_ms"] = dur.Milliseconds()
	if err != nil {
		f["error"] = err.Error()
	}
	r := o11y.LogRecord{
		TimeUnixNs:   time.Now().UnixNano(),
		Severity:     o11y.SeverityInfo,
		SeverityText: "info",
		Body:         "request",
		Attributes:   f,
		TraceID:      tr.trace,
		SpanID:       tr.span,
	}
	if status >= 500 {
		r.Severity, r.SeverityText = o11y.SeverityError, "error"
	}
	return r
}

// isOps reports whether a path belongs to the ops surface rather than to the
// app. Stated over the same three constants the ops app registers, so a path
// cannot be excluded from the numbers without being one of the paths that
// exists.
func isOps(path string) bool {
	return path == HealthPath || path == ReadyPath || path == MetricsPath
}

// routeOf is the matched route TEMPLATE, or "" when the request matched no
// route of this app.
//
// It is read after the chain has run, because that is when the router has
// resolved a leaf; read on the way in it is still this middleware's own
// registration. A request that matched nothing reports the empty route rather
// than its path, which keeps a scan for unrouted addresses — the thing 404s are
// worth measuring for — one query on one bounded label instead of a cardinality
// bomb keyed on whatever a client sent.
//
// "Matched nothing" is read off the ERROR rather than off the route, because
// the route cannot answer it: an unmatched request leaves this middleware's own
// registration in place, and that registration is a route whose path is "/" and
// whose method is the request's — indistinguishable from a genuine leaf at the
// root. The router says so exactly once, by returning [fiber.ErrNotFound] from
// the chain, and that is a different value from the not-found a HANDLER returns
// for a row it could not find. The first matched no route; the second matched
// one and answered.
func routeOf(c *Ctx, err error) string {
	if errors.Is(err, fiber.ErrNotFound) {
		return ""
	}
	r := c.fc.Route()
	if r == nil {
		return ""
	}
	return strings.Clone(r.Path)
}

// outcome is the status this request will be answered with.
//
// It cannot simply be read off the response, and that is the subtle part: a
// handler that returns an error has not written a status yet — fiber's error
// chain writes it after this middleware has already unwound — so the response
// still holds the default 200. Reporting that would record every refusal and
// every failure as a success, which is precisely the direction an error must
// never be wrong in.
//
// So the rules are the ones [errorHandler] applies, stated here over the same
// two error types, and the number reported is therefore the number written.
func outcome(c *Ctx, err error) int {
	if err == nil {
		return c.fc.Response().StatusCode()
	}
	if he, ok := asHTTPError(err); ok && he.Status != 0 {
		return he.Status
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return 500
}

// observe moves the RED numbers. One counter and one histogram, both keyed on
// bounded labels, both moved exactly once per request.
func (t *telemetry) observe(method, route string, status int, dur time.Duration) {
	if t.off {
		return
	}
	t.requests.With(metric.Labels{
		"method": method,
		"route":  route,
		"status": strconv.Itoa(status),
	}).Inc()
	t.duration.With(metric.Labels{
		"method": method,
		"route":  route,
	}).Observe(dur.Seconds())
}

// describe returns the logger the request runs under: the app's own logger plus
// what is true of this request and nothing more.
//
// The caller is included WHEN THE ENVIRONMENT PARKED ONE. Identity arrives as
// the gateway's assertion in headers a service forwards but cannot mint (see
// [Caller]), so a direct call with no gateway in front has none — and the
// honest report of that is an absent field, never an empty one. "Said nothing"
// and "said empty" have to stay distinguishable in a log that is used to answer
// who did something.
func describe(base luxlog.Logger, c *Ctx, tr trace, method, path string) luxlog.Logger {
	fields := []any{
		"method", method,
		"path", path,
		"trace", tr.trace,
		"span", tr.span,
	}
	if v := c.fc.Get(HeaderRequestID); v != "" {
		fields = append(fields, "request", strings.Clone(v))
	}
	if v := c.fc.Get(HeaderOrg); v != "" {
		fields = append(fields, "org", strings.Clone(v))
	}
	if v := c.fc.Get(HeaderUser); v != "" {
		fields = append(fields, "user", strings.Clone(v))
	}
	// Where the call came from, as [Caller.IP] resolves it — the socket peer
	// unless this deployment has explicitly named the proxies it believes. The
	// resolution is not repeated here: an address the log line derived by its own
	// rule would be the one field on the line that disagrees with every other
	// surface, and it would disagree in the direction of believing a header
	// anyone can set.
	if ip := CallerOf(c.Forward()).IP; ip != "" {
		fields = append(fields, "ip", ip)
	}
	return base.New(fields...)
}

// emit writes the one line per request.
//
// The level is the STATUS, never merely the presence of an error, so a reader
// filtering at Error sees exactly the requests this service failed. A refusal
// is an error value in Go and a normal outcome in HTTP: a handler returns
// ErrForbidden to say no, and logging that at Error is how an error level stops
// selecting anything worth waking up for. The error's text is still reported
// when there is one — it is the most useful field on the line — it just does
// not decide the level.
func emit(log luxlog.Logger, status int, dur time.Duration, err error) {
	fields := []any{"status", status, "duration_ms", dur.Milliseconds()}
	if err != nil {
		fields = append(fields, "error", err.Error())
	}
	if status >= 500 {
		log.Error("request", fields...)
		return
	}
	log.Info("request", fields...)
}

// trace is this request's position in a distributed trace: the trace it belongs
// to, the span this hop is, and the span it answers to.
type trace struct {
	trace  string
	span   string
	parent string
}

// traceOf establishes the request's trace context and writes it back onto the
// request, which is what makes it propagate.
//
// Writing it back is the whole mechanism: [forwardIdentity] copies headers off
// the inbound request onto an outbound [Call], so a handler that calls another
// service carries this hop's identifiers without touching them, and the callee
// reads them here as its parent. A trace therefore assembles itself across a
// fleet of zip apps with nothing in any handler.
//
// A request that arrives with no trace context, or with one this cannot parse,
// STARTS a trace rather than joining none. That is the direction that keeps a
// trace complete at the edge, where the first zip app to see a browser's
// request is the one that has to begin it.
func traceOf(c *Ctx) trace {
	t := trace{}
	if id, parent, ok := parseTrace(c.fc.Get(HeaderTrace)); ok {
		t.trace, t.parent = id, parent
	} else {
		t.trace = mint(16)
	}
	t.span = mint(8)
	c.fc.Request().Header.Set(HeaderTrace, traceVersion+"-"+t.trace+"-"+t.span+"-01")
	return t
}

// parseTrace reads a W3C traceparent, returning the trace id and the span id
// that becomes this hop's parent.
//
// Every field is checked for length and hex-ness, and the all-zero ids are
// rejected, because the spec says they are invalid and something that emits
// them is emitting a placeholder. Accepting one would attach real spans to an
// id that every other broken sender also uses, which is worse than starting a
// fresh trace: it merges unrelated requests into one enormous trace.
func parseTrace(v string) (id, parent string, ok bool) {
	if len(v) < 55 {
		return "", "", false
	}
	parts := strings.Split(v[:55], "-")
	if len(parts) != 4 || parts[0] != traceVersion {
		return "", "", false
	}
	id, parent = parts[1], parts[2]
	if len(id) != 32 || len(parent) != 16 || !isHex(id) || !isHex(parent) {
		return "", "", false
	}
	if strings.Trim(id, "0") == "" || strings.Trim(parent, "0") == "" {
		return "", "", false
	}
	return id, parent, true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// mint returns n random bytes as hex. crypto/rand because a trace id has to be
// unique across a fleet, and a process-local seed is the one input every
// replica of a deployment shares.
func mint(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// gather takes the registry's snapshot, refreshing what zip knows about itself
// first so those numbers are read at the moment they are asked for rather than
// whenever they last happened to change.
func (a *App) gather() ([]*metric.MetricFamily, error) {
	t := a.telemetry
	if t.off {
		return nil, nil
	}
	if a.listening.Load() > 0 {
		t.up.Set(1)
	} else {
		t.up.Set(0)
	}
	t.uptime.Set(time.Since(a.born).Seconds())
	t.ops.Set(float64(len(a.Registry())))
	t.routes.Set(float64(len(a.router().GetRoutes(false))))
	for _, s := range a.Plugins() {
		v := 0.0
		if s.Running {
			v = 1
		}
		t.plugins.With(metric.Labels{"name": s.Name}).Set(v)
	}
	return t.registry.Gather()
}

// exports reports whether anything is collected for shipment. A process with no
// span or log address still measures and still logs; it just does not fill a
// buffer nothing will drain.
func (t *telemetry) exports() bool { return t.ship }

// telemetryHandler is the report as fiber wants it, or nil when this app makes
// none. Nil is what [App.materialise] checks, so an app with telemetry off
// carries no handler at all rather than one that returns early — the difference
// is one indirection per request on every route.
func (a *App) telemetryHandler() fiber.Handler {
	if a.telemetry.off {
		return nil
	}
	return toFiberHandler(a, a.report())
}

// Gather makes the App a metric.Gatherer, which is the whole of what the
// exporter needs from it. Stating it as the standard interface rather than a
// method of our own is what lets the numbers go out through the exporter
// luxfi/metric already ships, instead of a second one written here.
func (a *App) Gather() ([]*metric.MetricFamily, error) { return a.gather() }

// Metrics is where an app registers instruments of its own — a domain gauge, a
// queue depth, a fleet probe — into the same registry the framework's request
// instruments live in. Everything registered here rides the same /metrics page
// and the same export as the built-in numbers: one registry per program, one
// road out, nothing extra to configure. The standard Registerer is the whole
// surface, so a caller holds no framework type and the registry stays owned
// here.
func (a *App) Metrics() metric.Registerer { return a.telemetry.registry }

// exportInterval is how often the numbers go out. Fifteen seconds is the
// scrape interval the fleet already uses, so a push and a pull of the same
// registry describe the same time slices and a dashboard does not change shape
// depending on which way the data arrived.
const exportInterval = 15 * time.Second

// export ships this app's telemetry to the collector until it shuts down.
//
// It is a PUSH because that is what the receiving side accepts: o11y ingests
// what services send it and scrapes nothing (its own prober checks liveness,
// not metrics). The alternative — every one of 125 plugin processes discovered
// and scraped — is a service registry we would have to run in order to learn
// what we already know at the moment a process starts serving.
//
// One address and one connection per signal, mirroring the ears the collector
// binds — a signal that saturates or is firewalled takes only itself down. A
// signal with no address gets no exporter at all, so an app that reports nowhere
// starts no goroutine, opens no connection and takes no port: it returns right
// here. Metrics ride the exporter luxfi/metric already ships, because that is
// this same shape plus the family translation, and a second implementation here
// would be a second thing to keep in step with the collector.
//
// Nothing here can fail loudly. A collector that is down, moved or not yet
// deployed must not take an app with it, so every failure is a debug line and
// the next tick tries again.
func (a *App) export() {
	t := a.telemetry
	if t.off || !t.to.any() {
		return
	}

	stop := make(chan struct{})
	var closers []func(context.Context) error

	// Nothing dials here. Each exporter connects when it first has something to
	// send, and reconnects whenever it has none, so a collector that starts
	// after this program is picked up without a restart.
	var spanOut, logOut *o11y.Export
	if t.to.spans != "" {
		spanOut = o11y.New(o11y.Spans, t.to.spans, a.cfg.AppName, a.logger.Info)
		closers = append(closers, func(context.Context) error { spanOut.Close(); return nil })
	}
	if t.to.logs != "" {
		logOut = o11y.New(o11y.Logs, t.to.logs, a.cfg.AppName, a.logger.Info)
		closers = append(closers, func(context.Context) error { logOut.Close(); return nil })
	}

	// Metrics ride the exporter luxfi/metric already ships, which is this same
	// shape plus the family translation. It is built only when there is an
	// address, because building it starts a node and takes a port immediately.
	var metricOut *metric.ZAPExporter
	if t.to.metrics != "" {
		out, err := metric.NewZAPExporter(metric.ZAPExporterConfig{
			Endpoint: t.to.metrics,
			AppName:  a.cfg.AppName,
		})
		if err != nil {
			a.logger.Error("zip telemetry metrics export unavailable", "err", err)
		} else {
			metricOut = out
			closers = append(closers, out.Shutdown)
		}
	}

	a.OnShutdown(func(ctx context.Context) error {
		close(stop)
		// One last drain, so the requests answered between the final tick and
		// the signal are not the ones an operator most wants and cannot have.
		a.flush(ctx, spanOut, logOut)
		for _, c := range closers {
			_ = c(ctx)
		}
		return nil
	})
	go func() {
		t := time.NewTicker(exportInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				ctx := context.Background()
				a.flush(ctx, spanOut, logOut)
				if metricOut != nil {
					if err := metricOut.ExportGatherer(ctx, a); err != nil {
						a.logger.Debug("zip telemetry export failed", "signal", "metrics", "err", err)
					}
				}
			}
		}
	}()
}

// flush ships whatever the boundary has collected since the last tick.
//
// The buffer is drained ONCE and both batches are built from that one drain, so
// a span and the line describing the same request leave together. Draining per
// signal would let a shutdown deliver the spans and lose the lines.
//
// The batch names the app and nothing else. The collector lifts a batch's app
// name into service.name when the resource does not already carry one, so
// saying it twice would be two places to disagree about what this program is
// called.
func (a *App) flush(ctx context.Context, spanOut, logOut *o11y.Export) {
	if !a.telemetry.ship {
		return
	}
	spans, records := a.telemetry.drain()
	if spanOut != nil {
		spanOut.Spans(ctx, o11y.SpanBatch{Spans: spans})
	}
	if logOut != nil {
		logOut.Logs(ctx, o11y.LogBatch{Records: records})
	}
}
