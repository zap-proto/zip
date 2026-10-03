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
	app.Group("/legacy").Raw(MethodAll, "/*", AdaptNetHTTP(h))
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

// AN UPGRADE REACHES THE CONNECTION. A net/http WebSocket server takes the
// socket with http.Hijacker and writes the 101 itself; without Hijack on the
// writer coder/websocket answers 501 to every upgrade, which is how the talk
// socket behind api.hanzo.ai refused every browser that reached it.
func TestAdaptNetHTTP_AnUpgradeTakesTheConnection(t *testing.T) {
	base := streamApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "ok" {
			http.Error(w, "a valid ticket is required", http.StatusUnauthorized)
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		line, _ := brw.ReadString('\n')
		_, _ = brw.WriteString("echo " + line)
		_ = brw.Flush()
	})
	addr := strings.TrimPrefix(base, "http://")

	dial := func(ticket string) (net.Conn, *bufio.Reader) {
		t.Helper()
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "GET /legacy/socket?ticket=%s HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n", ticket)
		return conn, bufio.NewReader(conn)
	}

	conn, rd := dial("ok")
	status, _ := rd.ReadString('\n')
	if !strings.HasPrefix(status, "HTTP/1.1 101") {
		t.Fatalf("upgrade answered %q, want 101 from the handler", status)
	}
	for line, _ := rd.ReadString('\n'); line != "\r\n" && line != ""; line, _ = rd.ReadString('\n') {
	}
	fmt.Fprint(conn, "ping\n")
	if got, _ := rd.ReadString('\n'); got != "echo ping\n" {
		t.Fatalf("the upgraded connection answered %q, want %q", got, "echo ping\n")
	}

	// A handler that declines the upgrade answers an ordinary reply.
	_, rd = dial("no")
	status, _ = rd.ReadString('\n')
	if !strings.HasPrefix(status, "HTTP/1.1 401") {
		t.Fatalf("a refused upgrade answered %q, want 401", status)
	}
	rest, _ := io.ReadAll(rd)
	if !strings.Contains(string(rest), "a valid ticket is required") {
		t.Fatalf("the refusal lost its body: %q", rest)
	}
}

// THE 101 GOES OUT WHEN IT IS WRITTEN. coder/websocket sets its headers, calls
// WriteHeader(101) and only then hijacks, relying on net/http to have sent an
// informational status at once. Held until the handler returned, the client never
// saw the switch and the handshake hung — the talk socket's second failure.
func TestAdaptNetHTTP_ASwitchWrittenBeforeTheHijackIsSent(t *testing.T) {
	base := streamApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Upgrade", "echo")
		w.Header().Set("Connection", "Upgrade")
		w.WriteHeader(http.StatusSwitchingProtocols)
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		line, _ := brw.ReadString('\n')
		_, _ = brw.WriteString("echo " + line)
		_ = brw.Flush()
	})
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(conn, "GET /legacy/socket HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	rd := bufio.NewReader(conn)
	status, err := rd.ReadString('\n')
	if err != nil || !strings.HasPrefix(status, "HTTP/1.1 101") {
		t.Fatalf("the switch answered %q (%v), want a 101 before any frame", status, err)
	}
	head := map[string]bool{}
	for line, _ := rd.ReadString('\n'); line != "\r\n" && line != ""; line, _ = rd.ReadString('\n') {
		head[strings.ToLower(strings.TrimSpace(line))] = true
	}
	if !head["upgrade: echo"] {
		t.Fatalf("the 101 lost the handler's headers: %v", head)
	}
	fmt.Fprint(conn, "ping\n")
	if got, _ := rd.ReadString('\n'); got != "echo ping\n" {
		t.Fatalf("after the switch the connection answered %q", got)
	}
}

// A HEADER KEPT IS A HEADER KEPT. A handler stores the bearer one request
// presented; the next request on the same connection presents another of the
// same length. The stored one must still read as it was — it was handed a Go
// string, and a string does not change under its holder.
func TestAdaptNetHTTP_AKeptHeaderSurvivesTheNextRequest(t *testing.T) {
	kept := make(chan [2]string, 2)
	base := streamApp(t, func(w http.ResponseWriter, r *http.Request) {
		kept <- [2]string{r.Header.Get("Authorization"), r.URL.Query().Get("ticket")}
		_, _ = io.WriteString(w, "ok")
	})
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	rd := bufio.NewReader(conn)
	ask := func(bearer, ticket string) {
		fmt.Fprintf(conn, "GET /legacy/session?ticket=%s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\n\r\n", ticket, bearer)
		resp, err := http.ReadResponse(rd, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	ask("aaaaaaaaaaaaaaaa", "first-ticket")
	first := <-kept
	ask("bbbbbbbbbbbbbbbb", "other-ticket")
	<-kept
	if first[0] != "Bearer aaaaaaaaaaaaaaaa" || first[1] != "first-ticket" {
		t.Fatalf("the first request's header and ticket now read %q and %q", first[0], first[1])
	}
}
