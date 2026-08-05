// Package o11y is the shape of what a zip app sends to the collector, and the
// one place that knows it.
//
// A batch is JSON, the JSON rides in a ZAP envelope tagged with the signal's
// message type, and the envelope goes down a ZAP connection. That is the whole
// transport: no OpenTelemetry SDK, no protobuf, no gRPC, no OTLP. zip is the
// base every Hanzo program is built on, so a dependency taken here is a
// dependency taken in all of them, and an SDK is a large one to take in order
// to send two JSON documents.
//
// # The shapes are copied deliberately, not imported
//
// SpanBatch and LogBatch are field-for-field what hanzoai/o11y decodes in
// pkg/zapreceiver and pkg/zaplogreceiver. They are re-declared here rather than
// imported, and that is the same choice the receiving side already made — its
// own doc says it re-declares shapes rather than importing the producer
// libraries. Importing across the boundary would also be circular: o11y already
// requires zip, so zip requiring o11y would leave neither end able to move
// without the other. Two declarations of one contract is the cost of letting
// the two ends ship independently, and contract/ — a module of its own, so no
// consumer of zip inherits the collector's dependencies — is what holds them
// together, by feeding this encoder's output to the real decoder.
//
// # The address is the switch, on both ends
//
// A signal with no address is a signal this program does not report. That is
// not a convenience: it is the same rule the collector states for receiving —
// "THE ADDRESS IS THE SWITCH ... an empty address means that signal is not
// received in this process" — so one sentence describes both halves, and there
// is no enable flag anywhere that can disagree with an address.
//
// An address is host:port. The collector reads a port out of its listen address
// and binds TCP, so host:port is the only form it can serve and the only form
// worth sending to.
package o11y

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/luxfi/zap"
)

// The signal message types, in the ZAP envelope's flags. They are a fleet-wide
// registry rather than this package's choice: 1 is spans, 2 is metrics, 3 is
// logs, and every deployed collector switches on them. Append only —
// renumbering breaks every receiver at once. Metrics is stated but never sent
// from here: luxfi/metric owns that encoder, and the number is written down so
// nothing invents a second meaning for it.
const (
	MsgSpan   uint16 = 1
	MsgMetric uint16 = 2
	MsgLog    uint16 = 3
)

// SpanBatch is one shipment of spans. Mirrors o11y zapreceiver.SpanBatch.
//
// Resource holds what is true of the SENDER for every span in the batch. It
// deliberately carries no caller identity: the collector reads resource["org"]
// as the TENANT that owns the telemetry — whose rows these become and who may
// read them — which is the operator of this program, not whoever it answered.
// The request's org is an attribute, where a per-request value belongs.
type SpanBatch struct {
	AppName  string            `json:"appName,omitempty"`
	Version  string            `json:"version,omitempty"`
	Resource map[string]string `json:"resource,omitempty"`
	Spans    []Span            `json:"spans"`
}

// Span is one operation. Mirrors o11y zapreceiver.Span.
//
// Kind and StatusCode are read through the collector's normEnum, which
// lowercases and strips a leading "span_kind_" / "status_code_". So "server"
// and "SPAN_KIND_SERVER" are the same value, and the plain lowercase spelling
// is the one used here.
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
// Severity is the severity NUMBER, which the collector clamps to a uint8 and
// stores beside the text: 1 trace, 5 debug, 9 info, 13 warn, 17 error, 21
// fatal. TraceID and SpanID are what join a line to the span it happened under,
// and they are the reason a log address is worth setting at all.
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

// The severity numbers, named so a mapping reads as a mapping.
const (
	SeverityDebug = 5
	SeverityInfo  = 9
	SeverityWarn  = 13
	SeverityError = 17
)

// Signal binds a signal's name to the message type it travels under, so a call
// site cannot pair the two wrongly. Metrics is absent because luxfi/metric
// ships that signal itself.
type Signal struct {
	Name string // "spans" or "logs" — as the collector's own config names them
	Msg  uint16 // the ZAP message type
}

var (
	Spans = Signal{Name: "spans", Msg: MsgSpan}
	Logs  = Signal{Name: "logs", Msg: MsgLog}
)

// Export ships one signal's batches to the collector at one address.
//
// Nothing is dialled until there is something to send, and a failure to send
// costs the caller nothing.
//
// Fire and forget, always. Telemetry must never be able to stop the program it
// describes, so a batch that cannot be delivered is DROPPED rather than held —
// holding it is how a producer grows without bound while nobody is listening.
type Export struct {
	sig     Signal
	addr    string
	appName string
	say     func(string, ...any)

	mu     sync.Mutex
	node   *zap.Node
	peer   string
	up     bool // last known state, so reporting is edge-triggered
	said   bool // whether a state has been reported at all yet
	closed bool
}

// New prepares an exporter for an address the caller has already decided to
// report to. It binds nothing, dials nothing and cannot fail: a collector that
// is absent, or that starts later, is the ordinary case.
//
// say is how it reports, and it is the APP's own logger rather than one of this
// package's choosing — an exporter's two lines are the program telling you
// something about itself, and they belong in the same stream, at the same
// level, with the same fields as everything else the program says.
func New(sig Signal, addr, appName string, say func(string, ...any)) *Export {
	if say == nil {
		say = func(string, ...any) {}
	}
	return &Export{sig: sig, addr: addr, appName: appName, say: say}
}

// connect dials the collector, and redials after a connection is lost.
//
// The address is passed to luxfi/zap untouched, which reads its form to choose
// a network — the same discipline zip applies when serving, and the reason
// nothing here inspects the string.
func (e *Export) connect() error {
	if e.node == nil {
		n := zap.NewNode(zap.NodeConfig{
			NodeID:      "o11y-" + e.appName + "-" + e.sig.Name,
			ServiceType: "_o11y._tcp",
			// The node's own chatter — started, peer connected, peer gone — is
			// this package's internals, not the program's news. What an operator
			// needs is the two edges in report, in the program's own log.
			Logger: slog.New(slog.DiscardHandler),
			// Discovery would make an exporter announce itself and hunt for
			// peers. It has one peer, at the one address it was given.
			NoDiscovery: true,
		})
		if err := n.Start(); err != nil {
			return fmt.Errorf("start node: %w", err)
		}
		e.node = n
	}
	if err := e.node.ConnectDirect(e.addr); err != nil {
		return fmt.Errorf("%s: %w", e.addr, err)
	}
	peers := e.node.Peers()
	if len(peers) == 0 {
		return fmt.Errorf("%s: connected but no peer", e.addr)
	}
	e.peer = peers[0]
	return nil
}

// report says when export STARTS working and when it STOPS, and nothing in
// between.
//
// Edge-triggered because the alternative is a line per failed flush forever on
// every box whose collector is down — which trains everyone to ignore the
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
	e.say("telemetry export stopped", "signal", e.sig.Name, "addr", e.addr, "reason", why.Error())
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

// send is the one path a batch takes: dial, marshal, frame, write.
//
// Every failure ends here. A caller cannot act on a telemetry error — there is
// nothing sensible for a request handler to do about a collector being down —
// so none is returned, and the only trace of it is the edge-triggered line.
func (e *Export) send(ctx context.Context, msg uint16, batch any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	// Dial lazily, and redial whenever there is no connection: the collector may
	// have appeared since the last attempt, which is the normal case when start
	// order puts this program first.
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
		// The connection is gone. Forget the peer so the next batch redials,
		// rather than writing into a socket that will never drain.
		e.peer = ""
		e.report(false, err)
		return
	}
	e.report(true, nil)
}

// frame wraps a JSON payload in the ZAP envelope every collector receiver
// expects: a root object whose field 0 is the payload bytes, with the signal's
// message type in the upper 8 bits of the envelope flags.
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
