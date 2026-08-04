package otlz_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/luxfi/zap"
	"github.com/zap-proto/zip/internal/otlz"
)

// The shapes are pinned as BYTES, not against o11y's Go types.
//
// Decoding through the collector's own structs would be the stronger proof and
// it was measured rather than assumed: importing github.com/hanzoai/o11y for
// tests takes zip's go.sum from 149 lines to 720 and its module graph to 1145
// modules, pulling in Kubernetes apimachinery and klog. zip is the base every
// Hanzo program builds on, and a module graph is shared with every consumer, so
// that cost is paid by all 125 plugins to run a test here.
//
// So the field names below are transcribed from the receiver's own struct tags —
// o11y pkg/zapreceiver (SpanBatch, Span, SpanEvent) and pkg/zaplogreceiver
// (LogBatch, LogRecord) at v1.5.58 — and asserted exactly. If either end renames
// a field, this fails. That is the whole job: the two declarations are
// deliberate copies of one contract, and something has to hold them together.

// spanKeys is every JSON name the span receiver decodes, batch and span.
var spanKeys = struct{ batch, span, event []string }{
	batch: []string{"appName", "version", "resource", "spans"},
	span: []string{
		"traceId", "spanId", "parentSpanId", "name", "kind",
		"startUnixNs", "endUnixNs", "attributes", "events",
		"statusCode", "statusMessage",
	},
	event: []string{"name", "timeUnixNs", "attributes"},
}

// logKeys is every JSON name the log receiver decodes.
var logKeys = struct{ batch, record []string }{
	batch: []string{"appName", "version", "resource", "records"},
	record: []string{
		"timeUnixNs", "observedTimeUnixNs", "severity", "severityText",
		"body", "attributes", "traceId", "spanId", "eventName",
	},
}

// keysOf marshals v and returns its top-level object keys.
func keysOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// assertKeys checks the emitted object carries exactly want and nothing else.
// Extra keys matter as much as missing ones: a field this side invented is a
// field the collector drops, and silently.
func assertKeys(t *testing.T, what string, got map[string]bool, want []string) {
	t.Helper()
	for _, k := range want {
		if !got[k] {
			t.Errorf("%s: missing %q — the receiver decodes it", what, k)
		}
	}
	for k := range got {
		found := false
		for _, w := range want {
			if k == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: emits %q, which the receiver does not decode", what, k)
		}
	}
}

// filled builds values with every field set, so omitempty cannot hide a rename.
func filledSpan() otlz.SpanBatch {
	return otlz.SpanBatch{
		AppName:  "svc",
		Version:  "v1",
		Resource: map[string]string{"service.name": "svc"},
		Spans: []otlz.Span{{
			TraceID:      "0102030405060708090a0b0c0d0e0f10",
			SpanID:       "1112131415161718",
			ParentSpanID: "2122232425262728",
			Name:         "/v1/x",
			Kind:         "server",
			StartUnixNs:  1700000000000000000,
			EndUnixNs:    1700000000000001000,
			Attributes:   map[string]any{"http.method": "GET"},
			Events:       []otlz.SpanEvent{{Name: "e", TimeUnixNs: 1, Attributes: map[string]any{"k": "v"}}},
			StatusCode:   "Ok",
			StatusMsg:    "fine",
		}},
	}
}

func filledLog() otlz.LogBatch {
	return otlz.LogBatch{
		AppName:  "svc",
		Version:  "v1",
		Resource: map[string]string{"service.name": "svc"},
		Records: []otlz.LogRecord{{
			TimeUnixNs:         1700000000000000000,
			ObservedTimeUnixNs: 1700000000000000001,
			Severity:           otlz.SeverityInfo,
			SeverityText:       "info",
			Body:               "request",
			Attributes:         map[string]any{"http.method": "GET"},
			TraceID:            "0102030405060708090a0b0c0d0e0f10",
			SpanID:             "1112131415161718",
			EventName:          "request",
		}},
	}
}

func TestSpanShapeMatchesReceiver(t *testing.T) {
	b := filledSpan()
	assertKeys(t, "SpanBatch", keysOf(t, b), spanKeys.batch)
	assertKeys(t, "Span", keysOf(t, b.Spans[0]), spanKeys.span)
	assertKeys(t, "SpanEvent", keysOf(t, b.Spans[0].Events[0]), spanKeys.event)
}

func TestLogShapeMatchesReceiver(t *testing.T) {
	b := filledLog()
	assertKeys(t, "LogBatch", keysOf(t, b), logKeys.batch)
	assertKeys(t, "LogRecord", keysOf(t, b.Records[0]), logKeys.record)
}

// TestSignalNumbers pins the fleet-wide registry. These are not this package's
// to choose: every deployed collector switches on them, so a change here is a
// change to every receiver at once.
func TestSignalNumbers(t *testing.T) {
	if otlz.MsgSpan != 1 {
		t.Errorf("span message type = %d, want 1", otlz.MsgSpan)
	}
	if otlz.MsgLog != 3 {
		t.Errorf("log message type = %d, want 3", otlz.MsgLog)
	}
}

// ear is a ZAP listener that decodes one signal, standing in for o11y's
// receiver. It decodes the envelope exactly as the receiver does — payload out
// of field 0, message type out of the flags' upper byte — so what it accepts is
// what the collector accepts.
type ear struct {
	node *zap.Node
	mu   sync.Mutex
	got  [][]byte
	msgs []uint16
	seen chan struct{}
}

func listen(t *testing.T, addr string, msg uint16) *ear {
	t.Helper()
	e := &ear{seen: make(chan struct{}, 8)}
	e.node = zap.NewNode(zap.NodeConfig{
		NodeID:      "ear-" + addr,
		ServiceType: "_o11y._tcp",
		Address:     addr,
		NoDiscovery: true,
	})
	e.node.Handle(msg, func(_ context.Context, _ string, m *zap.Message) (*zap.Message, error) {
		// Read the payload exactly as o11y's receivers do: field 0 of the root.
		cp := append([]byte(nil), m.Root().Bytes(0)...)
		e.mu.Lock()
		e.got = append(e.got, cp)
		e.msgs = append(e.msgs, msg)
		e.mu.Unlock()
		select {
		case e.seen <- struct{}{}:
		default:
		}
		// No reply: the sender does not wait, so answering would be a frame
		// nothing reads.
		return nil, nil
	})
	if err := e.node.Start(); err != nil {
		t.Fatalf("ear on %s: %v", addr, err)
	}
	t.Cleanup(e.node.Stop)
	return e
}

func (e *ear) await(t *testing.T) []byte {
	t.Helper()
	select {
	case <-e.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived at the collector")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.got[len(e.got)-1]
}

// sock returns a short socket path. A unix socket path is capped near 104 bytes
// on darwin, and t.TempDir() alone can exceed it — which fails as "invalid
// argument" and looks like a bug in the code under test.
func sock(t *testing.T, name string) string {
	t.Helper()
	d, err := os.MkdirTemp("", "otlz")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, name)
}

// TestSpansOverSocket and its siblings are the transport claim: the SAME
// exporter reaches a collector over a unix socket, chosen by nothing but the
// form of the address. No scheme is parsed here and none is passed.
func TestSpansOverSocket(t *testing.T) {
	addr := sock(t, "s.sock")
	e := listen(t, addr, otlz.MsgSpan)

	x := otlz.New(otlz.Spans, addr, "svc", nil)
	defer x.Close()
	x.Spans(context.Background(), filledSpan())

	var got otlz.SpanBatch
	if err := json.Unmarshal(e.await(t), &got); err != nil {
		t.Fatalf("collector could not decode: %v", err)
	}
	if len(got.Spans) != 1 || got.Spans[0].TraceID != filledSpan().Spans[0].TraceID {
		t.Fatalf("span did not survive the socket: %+v", got.Spans)
	}
	if got.AppName != "svc" {
		t.Errorf("appName = %q, want svc — the exporter stamps it", got.AppName)
	}
}

func TestLogsOverSocket(t *testing.T) {
	addr := sock(t, "l.sock")
	e := listen(t, addr, otlz.MsgLog)

	x := otlz.New(otlz.Logs, addr, "svc", nil)
	defer x.Close()
	x.Logs(context.Background(), filledLog())

	var got otlz.LogBatch
	if err := json.Unmarshal(e.await(t), &got); err != nil {
		t.Fatalf("collector could not decode: %v", err)
	}
	if len(got.Records) != 1 || got.Records[0].Body != "request" {
		t.Fatalf("record did not survive the socket: %+v", got.Records)
	}
	if got.Records[0].TraceID == "" || got.Records[0].SpanID == "" {
		t.Error("the record lost its trace ids, which are the reason logs ship at all")
	}
}

// TestOverTCP proves the same exporter, unchanged, reaches a host:port
// collector. Together with the socket tests this is the whole transport claim:
// one code path, two transports, chosen by the address.
func TestOverTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	e := listen(t, addr, otlz.MsgSpan)
	x := otlz.New(otlz.Spans, addr, "svc", nil)
	defer x.Close()
	x.Spans(context.Background(), filledSpan())

	var got otlz.SpanBatch
	if err := json.Unmarshal(e.await(t), &got); err != nil {
		t.Fatalf("collector could not decode: %v", err)
	}
	if len(got.Spans) != 1 {
		t.Fatalf("span did not survive tcp: %+v", got.Spans)
	}
}

// TestUnreachableCollectorIsSurvivable is the fire-and-forget claim. An address
// nothing listens on must cost the caller nothing — no error to handle, no
// panic, no block — because telemetry may never be able to stop the program it
// describes.
func TestUnreachableCollectorIsSurvivable(t *testing.T) {
	x := otlz.New(otlz.Spans, sock(t, "nobody.sock"), "svc", nil)
	defer x.Close()
	done := make(chan struct{})
	go func() {
		x.Spans(context.Background(), filledSpan())
		x.Logs(context.Background(), filledLog())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("exporting to a dead collector blocked the caller")
	}
}

// TestEmptyBatchSendsNothing keeps idle services quiet: a tick with no traffic
// must not open a connection or write a frame.
func TestEmptyBatchSendsNothing(t *testing.T) {
	addr := sock(t, "quiet.sock")
	e := listen(t, addr, otlz.MsgSpan)
	x := otlz.New(otlz.Spans, addr, "svc", nil)
	defer x.Close()
	x.Spans(context.Background(), otlz.SpanBatch{})
	select {
	case <-e.seen:
		t.Fatal("an empty batch was sent")
	case <-time.After(300 * time.Millisecond):
	}
}

// --- the conventions ------------------------------------------------------

// TestConventionPrefersThePresentSocket is the zero-configuration claim: with
// the collector's socket on the runtime directory, that is where a report goes,
// and nothing was configured to make it so.
func TestConventionPrefersThePresentSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)

	want := filepath.Join(dir, "o11y-spans.sock")
	if got := otlz.Address(otlz.Spans, ""); got == want {
		t.Fatal("the socket was chosen before it existed; presence is the configuration")
	}
	// Create it, and the same call must now resolve to it.
	f, err := os.Create(want)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = f.Close()
	if got := otlz.Address(otlz.Spans, ""); got != want {
		t.Errorf("address = %q, want the socket %q now that it exists", got, want)
	}
}

// TestConventionFallsBackToLoopback pins the second rule: no socket, no
// configuration, and a report still has somewhere to go.
func TestConventionFallsBackToLoopback(t *testing.T) {
	dir, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)

	for _, c := range []struct {
		sig  otlz.Signal
		want string
	}{
		{otlz.Spans, "127.0.0.1:4317"},
		{otlz.Logs, "127.0.0.1:4318"},
		{otlz.Metrics, "127.0.0.1:4319"},
	} {
		if got := otlz.Address(c.sig, ""); got != c.want {
			t.Errorf("%s address = %q, want %q", c.sig.Name, got, c.want)
		}
	}
}

// TestOverrideWins keeps configuration available for the remote collector,
// without it ever being required.
func TestOverrideWins(t *testing.T) {
	dir, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	f, _ := os.Create(filepath.Join(dir, "o11y-spans.sock"))
	if f != nil {
		_ = f.Close()
	}
	if got := otlz.Address(otlz.Spans, "collector.example:4317"); got != "collector.example:4317" {
		t.Errorf("address = %q, want the override even with a socket present", got)
	}
}

// TestReportIsEdgeTriggered is the "must not spam" claim. A program with no
// collector answers requests forever; it must say so once, not once per flush.
func TestReportIsEdgeTriggered(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	say := func(msg string, _ ...any) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	}
	x := otlz.New(otlz.Spans, sock(t, "absent.sock"), "svc", say)
	defer x.Close()
	for range 10 {
		x.Spans(context.Background(), filledSpan())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 {
		t.Fatalf("ten failed exports produced %d lines, want exactly 1: %v", len(lines), lines)
	}
	if lines[0] != "telemetry export stopped" {
		t.Errorf("line = %q, want the stopped edge", lines[0])
	}
}

// TestReportSaysWhenExportStartsWorking is the other edge, and the one that
// matters for start order: a collector that appears later is picked up, and the
// program says so once.
func TestReportSaysWhenExportStartsWorking(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	say := func(msg string, _ ...any) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	}
	addr := sock(t, "late.sock")
	x := otlz.New(otlz.Spans, addr, "svc", say)
	defer x.Close()

	// Nothing is listening yet.
	x.Spans(context.Background(), filledSpan())

	// The collector starts AFTER the program did.
	listen(t, addr, otlz.MsgSpan)
	x.Spans(context.Background(), filledSpan())

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 2 || lines[0] != "telemetry export stopped" || lines[1] != "telemetry export working" {
		t.Fatalf("want stopped then working, got %v", lines)
	}
}
