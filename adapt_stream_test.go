package zip

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/fiber/v3"
)

// serve starts the app on a real socket. That is the point: fiber's in-memory
// test helper collects the whole response, so it cannot tell a stream from a
// buffer — the one distinction these tests exist to make.
func serveStream(t *testing.T, app *App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Fiber().Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = app.Fiber().Shutdown() })
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", ln.Addr().String(), 50*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "http://" + ln.Addr().String()
}

func streamApp(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	app := New(Config{DisableStartupMessage: true})
	app.Group("/legacy").All("/*", AdaptNetHTTP(h))
	return serveStream(t, app)
}

// Every streaming handler opens by asserting http.Flusher.
func TestAdaptNetHTTP_WriterIsAFlusher(t *testing.T) {
	base := streamApp(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			http.Error(w, "writer does not implement http.Flusher", 500)
			return
		}
		fmt.Fprint(w, "flushable")
	})
	res, err := http.Get(base + "/legacy/x")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "flushable" {
		t.Fatalf("status %d body %q", res.StatusCode, body)
	}
}

// A frame written before the handler returns must be readable before it returns.
func TestAdaptNetHTTP_FramesArriveBeforeTheHandlerReturns(t *testing.T) {
	release := make(chan struct{})
	base := streamApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release // still running
		fmt.Fprint(w, "data: second\n\n")
	})
	res, err := http.Get(base + "/legacy/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type %q — headers must be set before the body streams", ct)
	}
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(res.Body).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if !strings.Contains(line, "first") {
			t.Fatalf("first frame %q", line)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("nothing arrived while the handler was still running — still buffering")
	}
	close(release)
}

func TestAdaptNetHTTP_StatusAndHeadersSurvive(t *testing.T) {
	base := streamApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace", "abc")
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprint(w, `{"ok":1}`)
	})
	res, err := http.Get(base + "/legacy/j")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusTeapot || res.Header.Get("X-Trace") != "abc" || string(body) != `{"ok":1}` {
		t.Fatalf("status %d headers %v body %q", res.StatusCode, res.Header, body)
	}
}

func TestAdaptNetHTTP_SilentHandlerStillAnswers(t *testing.T) {
	base := streamApp(t, func(w http.ResponseWriter, r *http.Request) {})
	res, err := http.Get(base + "/legacy/q")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestAdaptNetHTTP_PanicClosesTheStream(t *testing.T) {
	base := streamApp(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "partial")
		panic("boom")
	})
	res, err := http.Get(base + "/legacy/p")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	done := make(chan struct{})
	go func() { _, _ = io.ReadAll(res.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reader stranded after the handler panicked")
	}
}
