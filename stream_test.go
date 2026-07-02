package zip_test

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"

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
	app.Get("/events", func(c *zip.Ctx) error {
		return c.SendStreamWriter(func(w *bufio.Writer) {
			for i := 0; i < n; i++ {
				<-release
				_, _ = w.WriteString("data: e" + string(rune('0'+i)) + "\n\n")
				_ = w.Flush()
			}
		})
	})

	const addr = "127.0.0.1:19655"
	go func() { _ = app.Listen(addr) }() // bare addr = ZAP
	defer func() { _ = app.Shutdown() }()

	// Wait for the ZAP listener.
	tr := zaphttp.NewTransport(addr)
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
