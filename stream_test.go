package zip_test

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/http"

	"github.com/zap-proto/zip"
)

// TestListenZAP_Streams proves the streaming transport is wired THROUGH the
// framework: a zip route that uses c.SendStreamWriter (SSE) pushes each event
// over ListenZAP as it flushes — real server→client streaming over ZAP, end to
// end, with no per-handler transport code. This is the bidirectional brick made
// available to every zip app for free.
func TestListenZAP_Streams(t *testing.T) {
	const n = 3
	release := make(chan struct{}, n)

	app := zip.New(zip.Config{AppName: "streamer", DisableStartupMessage: true})
	app.Raw("GET", "/events", func(c *zip.Ctx) error {
		return c.SendStreamWriter(func(w *bufio.Writer) {
			for i := 0; i < n; i++ {
				<-release
				_, _ = w.WriteString("data: e" + string(rune('0'+i)) + "\n\n")
				_ = w.Flush()
			}
		})
	})

	addr := freeAddr(t)
	go func() { _ = app.Listen(addr) }() // bare addr = ZAP
	defer func() { _ = app.Shutdown() }()

	// Wait for the ZAP listener.
	tr := http.Dial("tcp", addr)
	defer tr.CloseIdleConnections()
	for i := 0; i < 50; i++ {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		req.SetRequestURI("/health")
		req.Header.SetMethod("GET")
		err := tr.Do(req, resp)
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		if err == nil {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI("/events")
	req.Header.SetMethod("GET")
	if err := tr.Do(req, resp); err != nil {
		t.Fatalf("Do /events: %v", err)
	}
	if !resp.IsBodyStream() {
		t.Fatal("zip route did not stream over ZAP — got a buffered response")
	}

	br := bufio.NewReader(resp.BodyStream())
	for i := 0; i < n; i++ {
		release <- struct{}{}
		got, err := readOneEvent(br)
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if want := "data: e" + string(rune('0'+i)); !strings.Contains(got, want) {
			t.Fatalf("event %d = %q, want %q", i, got, want)
		}
	}
}

func readOneEvent(br *bufio.Reader) (string, error) {
	ch := make(chan struct {
		s string
		e error
	}, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := br.ReadString('\n')
			b.WriteString(line)
			if err != nil {
				ch <- struct {
					s string
					e error
				}{b.String(), err}
				return
			}
			if line == "\n" && b.Len() > 1 {
				ch <- struct {
					s string
					e error
				}{b.String(), nil}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.s, r.e
	case <-time.After(2 * time.Second):
		return "", &streamTimeout{}
	}
}

type streamTimeout struct{}

func (*streamTimeout) Error() string { return "timed out waiting for streamed event over ZAP" }

// A typed op's event stream crosses ZAP the way an untyped one does: as
// zap-proto/http's head, data and end frames, each event as it is sent. A
// typed op that upgrades is answered 501 over ZAP, which carries no upgrade.
func TestListenZAP_TypedStream(t *testing.T) {
	release := make(chan struct{})
	app := zip.New(zip.Config{AppName: "typed-stream", DisableStartupMessage: true})
	app.Get("/v1/events", func(context.Context, *struct{}) (*zip.Sse[piece], error) {
		return &zip.Sse[piece]{Send: func(_ context.Context, emit func(zip.Event[piece]) error) error {
			for _, w := range []string{"a", "b"} {
				<-release
				if err := emit(zip.Event[piece]{Data: piece{Text: w}}); err != nil {
					return err
				}
			}
			return nil
		}}, nil
	})
	app.Get("/v1/rooms", room)

	addr := freeAddr(t)
	go func() { _ = app.Listen(addr) }() // bare addr = ZAP
	defer func() { _ = app.Shutdown() }()
	tr := http.Dial("tcp", addr)
	defer tr.CloseIdleConnections()
	do := func(path string) *fasthttp.Response {
		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		resp := fasthttp.AcquireResponse()
		req.SetRequestURI(path)
		req.Header.SetMethod("GET")
		var err error
		for i := 0; i < 50; i++ {
			if err = tr.Do(req, resp); err == nil {
				return resp
			}
			time.Sleep(40 * time.Millisecond)
		}
		t.Fatalf("GET %s over ZAP: %v", path, err)
		return nil
	}

	resp := do("/v1/events")
	defer fasthttp.ReleaseResponse(resp)
	if !resp.IsBodyStream() || string(resp.Header.ContentType()) != "text/event-stream" {
		t.Fatalf("not a stream: %q", resp.Header.ContentType())
	}
	br := bufio.NewReader(resp.BodyStream())
	for _, w := range []string{"a", "b"} {
		release <- struct{}{}
		got, err := readOneEvent(br)
		if err != nil || got != "data: {\"text\":\""+w+"\"}\n\n" {
			t.Fatalf("event %s = %q, %v", w, got, err)
		}
	}

	up := do("/v1/rooms?room=r1")
	defer fasthttp.ReleaseResponse(up)
	if up.StatusCode() != 501 {
		t.Errorf("an upgrade over ZAP answered %d, want 501", up.StatusCode())
	}
}
