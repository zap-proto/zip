package o11y_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/luxfi/zap"
	"github.com/zap-proto/zip/internal/o11y"
)

// What this package sends is proven against the collector's OWN decoder, in
// contract/ — a module of its own, so no consumer of zip inherits the
// collector's dependencies to run it. That proof is the one that can fail when a
// field is renamed on either side, so there is no transcribed copy of the
// receiver's field names here to drift away from it.
//
// What is left for this package's own tests is everything the contract module
// cannot see: that an unreachable collector costs the caller nothing, that an
// idle service is silent, and that the report about export itself is
// edge-triggered.

// filled builds values with every field set.
func filledSpan() o11y.SpanBatch {
	return o11y.SpanBatch{
		AppName:  "svc",
		Version:  "v1",
		Resource: map[string]string{"service.name": "svc"},
		Spans: []o11y.Span{{
			TraceID:      "0102030405060708090a0b0c0d0e0f10",
			SpanID:       "1112131415161718",
			ParentSpanID: "2122232425262728",
			Name:         "/v1/x",
			Kind:         "server",
			StartUnixNs:  1700000000000000000,
			EndUnixNs:    1700000000000001000,
			Attributes:   map[string]any{"http.request.method": "GET"},
			Events:       []o11y.SpanEvent{{Name: "e", TimeUnixNs: 1, Attributes: map[string]any{"k": "v"}}},
			StatusCode:   "ok",
			StatusMsg:    "fine",
		}},
	}
}

func filledLog() o11y.LogBatch {
	return o11y.LogBatch{
		AppName:  "svc",
		Version:  "v1",
		Resource: map[string]string{"service.name": "svc"},
		Records: []o11y.LogRecord{{
			TimeUnixNs:         1700000000000000000,
			ObservedTimeUnixNs: 1700000000000000001,
			Severity:           o11y.SeverityInfo,
			SeverityText:       "info",
			Body:               "request",
			Attributes:         map[string]any{"http.request.method": "GET"},
			TraceID:            "0102030405060708090a0b0c0d0e0f10",
			SpanID:             "1112131415161718",
			EventName:          "request",
		}},
	}
}

// TestSignalNumbers pins the fleet-wide registry. These are not this package's
// to choose: every deployed collector switches on them, so a change here is a
// change to every receiver at once.
func TestSignalNumbers(t *testing.T) {
	for _, c := range []struct {
		what string
		got  uint16
		want uint16
	}{
		{"span", o11y.MsgSpan, 1},
		{"metric", o11y.MsgMetric, 2},
		{"log", o11y.MsgLog, 3},
	} {
		if c.got != c.want {
			t.Errorf("%s message type = %d, want %d", c.what, c.got, c.want)
		}
	}
}

// ear is a ZAP listener that decodes one signal. It reads the envelope the way
// the collector's receivers do — payload out of field 0, message type out of the
// flags' upper byte.
type ear struct {
	node *zap.Node
	mu   sync.Mutex
	got  [][]byte
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
		cp := append([]byte(nil), m.Root().Bytes(0)...)
		e.mu.Lock()
		e.got = append(e.got, cp)
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

// free returns a loopback address nothing is listening on yet. host:port is the
// only form the collector serves, so it is the only form tested.
func free(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func TestSpansReachTheCollector(t *testing.T) {
	addr := free(t)
	e := listen(t, addr, o11y.MsgSpan)

	x := o11y.New(o11y.Spans, addr, "svc", nil)
	defer x.Close()
	x.Spans(context.Background(), filledSpan())

	var got o11y.SpanBatch
	if err := json.Unmarshal(e.await(t), &got); err != nil {
		t.Fatalf("collector could not decode: %v", err)
	}
	if len(got.Spans) != 1 || got.Spans[0].TraceID != filledSpan().Spans[0].TraceID {
		t.Fatalf("span did not survive the trip: %+v", got.Spans)
	}
	if got.AppName != "svc" {
		t.Errorf("appName = %q, want svc — the exporter stamps it", got.AppName)
	}
}

func TestLogsReachTheCollector(t *testing.T) {
	addr := free(t)
	e := listen(t, addr, o11y.MsgLog)

	x := o11y.New(o11y.Logs, addr, "svc", nil)
	defer x.Close()
	x.Logs(context.Background(), filledLog())

	var got o11y.LogBatch
	if err := json.Unmarshal(e.await(t), &got); err != nil {
		t.Fatalf("collector could not decode: %v", err)
	}
	if len(got.Records) != 1 || got.Records[0].Body != "request" {
		t.Fatalf("record did not survive the trip: %+v", got.Records)
	}
	if got.Records[0].TraceID == "" || got.Records[0].SpanID == "" {
		t.Error("the record lost its trace ids, which are the reason logs ship at all")
	}
}

// TestUnreachableCollectorIsSurvivable is the fire-and-forget claim. An address
// nothing listens on must cost the caller nothing — no error to handle, no
// panic, no block — because telemetry may never be able to stop the program it
// describes.
func TestUnreachableCollectorIsSurvivable(t *testing.T) {
	x := o11y.New(o11y.Spans, free(t), "svc", nil)
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
	addr := free(t)
	e := listen(t, addr, o11y.MsgSpan)
	x := o11y.New(o11y.Spans, addr, "svc", nil)
	defer x.Close()
	x.Spans(context.Background(), o11y.SpanBatch{})
	select {
	case <-e.seen:
		t.Fatal("an empty batch was sent")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestReportIsEdgeTriggered is the "must not spam" claim. A program whose
// collector is down answers requests forever; it must say so once, not once per
// flush.
func TestReportIsEdgeTriggered(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	say := func(msg string, _ ...any) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	}
	x := o11y.New(o11y.Spans, free(t), "svc", say)
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
	addr := free(t)
	x := o11y.New(o11y.Spans, addr, "svc", say)
	defer x.Close()

	// Nothing is listening yet.
	x.Spans(context.Background(), filledSpan())

	// The collector starts AFTER the program did.
	listen(t, addr, o11y.MsgSpan)
	x.Spans(context.Background(), filledSpan())

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 2 || lines[0] != "telemetry export stopped" || lines[1] != "telemetry export working" {
		t.Fatalf("want stopped then working, got %v", lines)
	}
}
