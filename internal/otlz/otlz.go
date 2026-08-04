// Package otlz is the shape of what a zip app sends to o11y, and the one place
// that knows it.
//
// It is telemetry over ZAP and nothing else: a batch is JSON, the JSON rides in
// a ZAP envelope tagged with the signal's message type, and the envelope goes
// down a ZAP connection. There is no OpenTelemetry SDK anywhere in it, no
// protobuf, no gRPC and no OTLP — which is the point. zip is the base every
// Hanzo program is built on, so a dependency here is a dependency in all of
// them, and the OTel SDK is a large one to take for three JSON documents.
//
// # The shapes are copied deliberately, not imported
//
// SpanBatch and LogBatch below are field-for-field what o11y's zapreceiver and
// zaplogreceiver decode. They are re-declared rather than imported, and that is
// the same choice the receiving side already made — zapingest's own doc says it
// re-declares shapes rather than importing the producer libraries. Importing
// across that boundary would make the emitter and the collector one build, so a
// collector could not be deployed without rebuilding every service that talks to
// it. Two declarations of one contract is the cost of letting the two ends ship
// independently, and the test beside this file is what keeps them honest.
//
// # The address chooses the transport, and nothing here branches on it
//
// An address is dialled by its FORM: a filesystem path is a unix socket,
// anything else is host:port TCP. That rule lives in luxfi/zap's Network, so
// this package passes the address through and never inspects it — the same
// discipline zip's own transportFor applies to serving. One consequence is worth
// stating: over a unix socket the kernel answers for the peer, so o11y learns
// WHICH process exported a batch as an attestation rather than a claim, exactly
// as zip documents for a Call. TCP loopback cannot say that — any process on the
// box can open the port and assert anything it likes about itself.
package otlz

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/luxfi/zap"
	"github.com/zap-proto/zip/internal/sock"
)

// The signal message types, in the ZAP envelope's flags. They are a fleet-wide
// registry rather than this package's choice: 1 is spans (luxfi/trace), 2 is
// metrics (luxfi/metric), 3 is logs (luxfi/log), and every deployed collector
// switches on them. Append only — renumbering breaks every receiver at once.
const (
	MsgSpan   uint16 = 1
	MsgMetric uint16 = 2
	MsgLog    uint16 = 3
)

// SpanBatch is one shipment of spans. Mirrors o11y zapreceiver.SpanBatch.
type SpanBatch struct {
	AppName  string            `json:"appName,omitempty"`
	Version  string            `json:"version,omitempty"`
	Resource map[string]string `json:"resource,omitempty"`
	Spans    []Span            `json:"spans"`
}

// Span is one operation. Mirrors o11y zapreceiver.Span.
//
// Kind and StatusCode are read through the collector's normEnum, which
// lowercases and strips a leading "span_kind_" / "status_code_", and maps
// "unset" to empty. So "server" and "SPAN_KIND_SERVER" are the same value, and
// the plain lowercase spelling is the one used here.
type Span struct {
	TraceID      string         `json:"traceId"`
	SpanID       string         `json:"spanId"`
	ParentSpanID string         `json:"parentSpanId,omitempty"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind,omitempty"`
	StartUnixNs  int64          `json:"startUnixNs"`
	EndUnixNs    int64          `json:"endUnixNs"`
	Attributes   map[string]any `json:"attributes,omitempty"`
	Events       []SpanEvent    `json:"events,omitempty"`
	StatusCode   string         `json:"statusCode,omitempty"`
	StatusMsg    string         `json:"statusMessage,omitempty"`
}

// SpanEvent is a point in time inside a span. Mirrors o11y zapreceiver.SpanEvent.
type SpanEvent struct {
	Name       string         `json:"name"`
	TimeUnixNs int64          `json:"timeUnixNs"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// LogBatch is one shipment of records. Mirrors o11y zaplogreceiver.LogBatch.
type LogBatch struct {
	AppName  string            `json:"appName,omitempty"`
	Version  string            `json:"version,omitempty"`
	Resource map[string]string `json:"resource,omitempty"`
	Records  []LogRecord       `json:"records"`
}

// LogRecord is one line. Mirrors o11y zaplogreceiver.LogRecord.
//
// Severity is the OTel severity NUMBER, which the collector clamps to a uint8
// and stores beside the text: 1 trace, 5 debug, 9 info, 13 warn, 17 error,
// 21 fatal. TraceID and SpanID are what join a line to the span it happened
// under, and they are the reason a log address is worth configuring at all.
type LogRecord struct {
	TimeUnixNs         int64          `json:"timeUnixNs"`
	ObservedTimeUnixNs int64          `json:"observedTimeUnixNs,omitempty"`
	Severity           int            `json:"severity,omitempty"`
	SeverityText       string         `json:"severityText,omitempty"`
	Body               string         `json:"body,omitempty"`
	Attributes         map[string]any `json:"attributes,omitempty"`
	TraceID            string         `json:"traceId,omitempty"`
	SpanID             string         `json:"spanId,omitempty"`
	EventName          string         `json:"eventName,omitempty"`
}

// The OTel severity numbers, named so a mapping reads as a mapping.
const (
	SeverityDebug = 5
	SeverityInfo  = 9
	SeverityWarn  = 13
	SeverityError = 17
)

// Signal is one of the three things a program reports, and everything that
// differs between them: the message type on the wire, the socket the collector
// serves it on, and the loopback port it falls back to.
//
// The three are separate because o11y receives them separately — one listener
// each, so "a signal must be able to fail, saturate or be firewalled on its
// own", as its config says. Stating them as one table is what keeps the
// producing side from inventing a fourth arrangement.
type Signal struct {
	Name string // "spans", "logs", "metrics" — as o11y's own config names them
	Msg  uint16 // the ZAP message type
	Sock string // the well-known service name its socket is reached by
	Port int    // the conventional loopback port
}

// The three signals, and the conventions that make telemetry need no
// configuration at all.
//
// A socket name is a service NAME in zip's existing scheme, so it resolves the
// way every other name does — <RuntimeDir()>/<name>.sock — and one environment
// variable already moves the whole fleet. The ports are the fleet's documented
// ones, which o11y's own config names in the same breath: spans 4317, logs 4318,
// metrics 4319.
var (
	Spans   = Signal{Name: "spans", Msg: MsgSpan, Sock: "o11y-spans", Port: 4317}
	Logs    = Signal{Name: "logs", Msg: MsgLog, Sock: "o11y-logs", Port: 4318}
	Metrics = Signal{Name: "metrics", Msg: MsgMetric, Sock: "o11y-metrics", Port: 4319}
)

// Address is where a signal goes, decided the way zip decides everything else:
// by convention, with configuration available and never required.
//
//	an override      a deployment that states one always wins
//	the socket       if the collector's socket EXISTS, it is the destination
//	loopback         otherwise the conventional port on this host
//
// The socket is preferred wherever it is present, and its PRESENCE is the whole
// of the configuration — o11y's ingest running in this pod creates it, and that
// is the fact worth acting on. It is also the better channel: over a unix socket
// the kernel attests which process sent a batch, so a collector learns the
// sender rather than being told, exactly as zip documents for a Call. On TCP
// loopback any process on the box can claim to be any service.
//
// It is re-decided on every attempt rather than resolved once, because plugin
// start order is nobody's business: a program that starts before the collector
// must pick it up when it appears, without a restart.
func Address(s Signal, override string) string {
	if o := strings.TrimSpace(override); o != "" {
		return o
	}
	sock := socket(s)
	if _, err := os.Stat(sock); err == nil {
		return sock
	}
	return "127.0.0.1:" + strconv.Itoa(s.Port)
}

// socket is the collector's socket path for one signal, in the fleet's one
// scheme — the same function zip.SocketPath is, so a collector serving by name
// and a program looking for it cannot disagree.
func socket(s Signal) string { return sock.Path(s.Sock) }

// Export ships one signal's batches to o11y.
//
// Nothing is dialled until there is something to send, and a failure to send
// costs the caller nothing. A program on a laptop with no collector anywhere
// runs exactly as a program in a pod does: it reports, the report goes nowhere,
// and it never says so more than once.
//
// Fire and forget, always. Telemetry must never be able to stop the program it
// describes, so a batch that cannot be delivered is DROPPED rather than held —
// holding it is how a producer grows without bound while nobody is listening.
type Export struct {
	sig      Signal
	override string
	appName  string
	say      func(string, ...any)

	mu     sync.Mutex
	node   *zap.Node
	peer   string
	addr   string // where the current connection went, for the report
	up     bool   // last known state, so reporting is edge-triggered
	said   bool   // whether a state has been reported at all yet
	closed bool
}

// New prepares an exporter. It binds nothing, dials nothing and cannot fail:
// a collector that is absent, or that starts later, is the ordinary case.
//
// say is how it reports, and it is the APP's own logger rather than one of this
// package's choosing — an exporter's two lines are the program telling you
// something about itself, and they belong in the same stream, at the same level,
// with the same fields as everything else the program says.
func New(sig Signal, override, appName string, say func(string, ...any)) *Export {
	if say == nil {
		say = func(string, ...any) {}
	}
	return &Export{sig: sig, override: override, appName: appName, say: say}
}

// connect resolves the destination afresh and dials it.
//
// The address is passed to luxfi/zap untouched, which reads its FORM to choose
// unix or tcp — so a socket path and a host:port arrive here identically and
// neither is special-cased. That is the same discipline zip applies when
// serving, and it is why growing a socket ear on the collector needs no change
// on this side.
func (e *Export) connect() error {
	if e.node == nil {
		n := zap.NewNode(zap.NodeConfig{
			NodeID:      "otlz-" + e.appName + "-" + e.sig.Name,
			ServiceType: "_o11y._tcp",
			// The node's own chatter — started, peer connected, peer gone — is
			// this package's internals, not the program's news. What an operator
			// needs is the two edges below, in the program's own log.
			Logger: slog.New(slog.DiscardHandler),
			// Discovery would make an exporter announce itself and hunt for peers.
			// It has one peer, and convention already names it.
			NoDiscovery: true,
		})
		if err := n.Start(); err != nil {
			return fmt.Errorf("start node: %w", err)
		}
		e.node = n
	}
	addr := Address(e.sig, e.override)
	if err := e.node.ConnectDirect(addr); err != nil {
		return fmt.Errorf("%s: %w", addr, err)
	}
	peers := e.node.Peers()
	if len(peers) == 0 {
		return fmt.Errorf("%s: connected but no peer", addr)
	}
	e.peer, e.addr = peers[0], addr
	return nil
}

// report says when export STARTS working and when it STOPS, and nothing in
// between.
//
// Edge-triggered because the alternative is a line per failed flush forever on
// every laptop that has no collector — which trains everyone to ignore the
// stream that is supposed to carry the one line that matters. The transition is
// the event; the steady state is not.
func (e *Export) report(up bool, why error) {
	if e.said && up == e.up {
		return
	}
	e.said, e.up = true, up
	if up {
		e.say("telemetry export working", "signal", e.sig.Name, "addr", e.addr)
		return
	}
	e.say("telemetry export stopped", "signal", e.sig.Name, "reason", why.Error())
}

// Spans ships a batch of spans.
func (e *Export) Spans(ctx context.Context, b SpanBatch) {
	if len(b.Spans) == 0 {
		return
	}
	b.AppName = e.appName
	e.send(ctx, MsgSpan, &b)
}

// Logs ships a batch of records.
func (e *Export) Logs(ctx context.Context, b LogBatch) {
	if len(b.Records) == 0 {
		return
	}
	b.AppName = e.appName
	e.send(ctx, MsgLog, &b)
}

// send is the one path a batch takes: resolve, dial, marshal, frame, write.
//
// Every failure ends here. A caller cannot act on a telemetry error — there is
// nothing sensible for a request handler to do about a collector being down — so
// none is returned, and the only trace of it is the edge-triggered line.
func (e *Export) send(ctx context.Context, msg uint16, batch any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	// Dial lazily, and re-probe whenever there is no connection: the collector
	// may have appeared since the last attempt, which is the normal case when
	// plugin start order puts this program first.
	if e.peer == "" {
		if err := e.connect(); err != nil {
			e.report(false, err)
			return
		}
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		e.report(false, err)
		return
	}
	m, err := zap.Parse(frame(msg, payload))
	if err != nil {
		e.report(false, err)
		return
	}
	if err := e.node.Send(ctx, e.peer, m); err != nil {
		// The connection is gone. Forget the peer so the next batch re-resolves
		// and redials, rather than writing into a socket that will never drain.
		e.peer = ""
		e.report(false, err)
		return
	}
	e.report(true, nil)
}

// frame wraps a JSON payload in the ZAP envelope every o11y receiver expects:
// a root object whose field 0 is the payload bytes, with the signal's message
// type in the upper 8 bits of the envelope flags.
func frame(msg uint16, payload []byte) []byte {
	const envelope = 16
	b := zap.NewBuilder(envelope + 64 + len(payload))
	root := b.StartObject(envelope)
	root.SetBytes(0, payload)
	root.FinishAsRoot()
	return b.FinishWithFlags(msg << 8)
}

// Close stops the node. Safe to call more than once, because shutdown paths run
// from more than one place.
func (e *Export) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	if e.node != nil {
		e.node.Stop()
	}
}
