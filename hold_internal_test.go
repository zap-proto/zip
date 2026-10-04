package zip

import (
	enc "encoding/binary"
	"io"
	"net"
	nethttp "net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
)

// A hold is released on every way a request can end, and the ceiling admits
// over itself only when nothing could ever make room. Each test here is a path
// that left a plugin held for good, or let a burst past the ceiling.

var (
	holdBinOnce sync.Once
	holdBin     []byte
	holdBinErr  error
)

func holdPlugin(t *testing.T) []byte {
	t.Helper()
	holdBinOnce.Do(func() {
		d, err := os.MkdirTemp("", "hold")
		if err != nil {
			holdBinErr = err
			return
		}
		defer os.RemoveAll(d)
		out := filepath.Join(d, "tp")
		cmd := exec.Command("go", "build", "-o", out, "./internal/testplugin")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			holdBinErr = err
			t.Logf("%s", b)
			return
		}
		holdBin, holdBinErr = os.ReadFile(out)
	})
	if holdBinErr != nil {
		t.Skipf("cannot build test plugin: %v", holdBinErr)
	}
	return holdBin
}

func holdHost(t *testing.T, warm int, specs ...Plugin) *App {
	t.Helper()
	app := New(Config{AppName: "host", DisableStartupMessage: true, Warm: warm})
	for _, p := range specs {
		l, err := Load(p, "/v1/"+p.Name)
		if err != nil {
			t.Fatal(err)
		}
		app.Use(l)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	return app
}

func lazy(t *testing.T, name string, idle time.Duration) Plugin {
	return Plugin{Name: name, Bin: holdPlugin(t), Dir: sockDir(t), Lazy: true, IdleAfter: idle}
}

func busyOf(a *App, name string) int64 {
	_, p := a.pluginNamed(name)
	if in := p.cur.Load(); in != nil {
		return in.busy.Load()
	}
	return -1
}

func running(a *App) int {
	n := 0
	for _, p := range a.pluginSet() {
		if p.cur.Load() != nil {
			n++
		}
	}
	return n
}

func get(t *testing.T, a *App, target string, hdr ...string) (int, string) {
	t.Helper()
	req, _ := nethttp.NewRequest("GET", target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := a.Test(req, TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Errorf("GET %s: %v", target, err) // Errorf: callers run it off the test goroutine
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func serveZAP(t *testing.T, a *App) string {
	t.Helper()
	sock := filepath.Join(sockDir(t), "h.sock")
	go func() { _ = a.Listen(sock) }()
	for range 300 {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("host never listened")
	return ""
}

func settle(limit time.Duration, ok func() bool) bool {
	for end := time.Now().Add(limit); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return ok()
}

// A ZAP caller that leaves before a streamed head is written releases its hold:
// the connection ending closes the body, and the body carries the hold.
func TestHold_ACallerWhoLeavesOverZAPReleases(t *testing.T) {
	app := holdHost(t, 1, lazy(t, "demo0", 100*time.Millisecond))
	sock := serveZAP(t, app)

	var req fasthttp.Request
	req.Header.SetMethod("GET")
	req.SetRequestURI("/v1/demo0/feed?drip=200ms")
	buf, err := zaphttp.AppendRequest([]byte{0, 0, 0, 0}, &req)
	if err != nil {
		t.Fatal(err)
	}
	enc.BigEndian.PutUint32(buf, uint32(len(buf)-4))
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(buf); err != nil {
		t.Fatal(err)
	}
	_ = c.Close() // gone before the plugin even starts

	if !settle(5*time.Second, func() bool { return busyOf(app, "demo0") >= 0 }) {
		t.Fatal("demo0 never started for the request")
	}
	if !settle(5*time.Second, func() bool { return busyOf(app, "demo0") == 0 }) {
		t.Fatalf("demo0 still holds %d requests after its only caller left", busyOf(app, "demo0"))
	}
}

// An upgrade cannot cross ZAP, whose server never runs a hijack. It is refused,
// and the hold it took is released.
func TestHold_AnUpgradeOverZAPIsRefused(t *testing.T) {
	app := holdHost(t, 1, lazy(t, "demo0", time.Hour))
	sock := serveZAP(t, app)

	tr := zaphttp.Dial("unix", sock)
	defer tr.CloseIdleConnections()
	var req fasthttp.Request
	var resp fasthttp.Response
	req.SetRequestURI("/v1/demo0/socket")
	req.Header.SetHost("box")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	if err := tr.Do(&req, &resp); err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.StatusCode() != 501 {
		t.Fatalf("status %d, want 501 for an upgrade over ZAP", resp.StatusCode())
	}
	if b := busyOf(app, "demo0"); b > 0 {
		t.Fatalf("demo0 holds %d requests after the refusal", b)
	}
}

// A plugin that declines an upgrade with a chunked reply is answered once the
// reply ends, and the hold is released. It used to read "until the connection
// closes", which a keep-alive plugin never does.
func TestHold_ADeclinedUpgradeWithAChunkedReplyEnds(t *testing.T) {
	app := holdHost(t, 1, lazy(t, "demo0", time.Hour))

	done := make(chan struct{})
	var status int
	var body string
	go func() {
		defer close(done)
		status, body = get(t, app, "/v1/demo0/feed?drip=300ms", "Connection", "Upgrade", "Upgrade", "websocket")
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("the declined upgrade never answered; demo0 holds %d", busyOf(app, "demo0"))
	}
	if status != 200 || !strings.HasSuffix(body, "done\n") {
		t.Fatalf("status %d body %q, want the plugin's whole reply", status, body)
	}
	if b := busyOf(app, "demo0"); b != 0 {
		t.Fatalf("demo0 holds %d after the reply ended", b)
	}
}

// A cold burst stays under the ceiling. Starts in flight are neither idle nor
// busy, and they used to read as "nothing may be stopped", which admitted every
// one of them over the ceiling.
func TestHold_AColdBurstStaysUnderTheCeiling(t *testing.T) {
	const warm, n = 2, 8
	var specs []Plugin
	for i := range n {
		specs = append(specs, lazy(t, "demo"+string(rune('0'+i)), time.Hour))
	}
	app := holdHost(t, warm, specs...)

	stop := make(chan struct{})
	peak := 0
	var mu sync.Mutex
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if r := running(app); r > peak {
				mu.Lock()
				peak = r
				mu.Unlock()
			}
			time.Sleep(time.Millisecond)
		}
	}()
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = get(t, app, "/v1/demo"+string(rune('0'+i))+"/version")
		}(i)
	}
	wg.Wait()
	close(stop)
	for i, c := range codes {
		if c != 200 {
			t.Errorf("demo%d answered %d", i, c)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if peak > warm {
		t.Fatalf("peak %d processes against a ceiling of %d", peak, warm)
	}
}

// When the coldest candidate cannot be stopped — its lock is held mid-Reload —
// room is taken from the next one rather than refused.
func TestHold_ALockedCandidateDoesNotBlockRoom(t *testing.T) {
	app := holdHost(t, 2,
		lazy(t, "demo0", time.Hour), lazy(t, "demo1", time.Hour), lazy(t, "demo2", time.Hour))
	for _, n := range []string{"demo0", "demo1"} {
		if c, _ := get(t, app, "/v1/"+n+"/version"); c != 200 {
			t.Fatalf("%s: %d", n, c)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, cold := app.pluginNamed("demo0")
	cold.mu.Lock() // demo0, the coldest, is mid-transition
	defer cold.mu.Unlock()

	if c, _ := get(t, app, "/v1/demo2/version"); c != 200 {
		t.Fatalf("demo2 answered %d with demo1 idle and stoppable", c)
	}
	_, warmer := app.pluginNamed("demo1")
	if warmer.cur.Load() != nil {
		t.Fatal("demo1 is still running; room came from nowhere")
	}
}

// Callers queued on one starter each wait their own bound, not one after
// another's: the wait is counted from when the caller arrived.
func TestHold_QueuedStartersWaitTogether(t *testing.T) {
	slow := lazy(t, "demo1", time.Hour)
	slow.Start = 400 * time.Millisecond
	app := holdHost(t, 1, lazy(t, "demo0", time.Hour), slow)

	go func() { _, _ = get(t, app, "/v1/demo0/version?sleep=4s") }()
	if !settle(5*time.Second, func() bool { return busyOf(app, "demo0") == 1 }) {
		t.Fatal("demo0 never became busy")
	}
	const callers = 5
	var wg sync.WaitGroup
	took := make([]time.Duration, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start := time.Now()
			if c, _ := get(t, app, "/v1/demo1/version"); c != 503 {
				t.Errorf("caller %d: %d, want 503 while the only process is busy", i, c)
			}
			took[i] = time.Since(start)
		}(i)
	}
	wg.Wait()
	for i, d := range took {
		if d > 1500*time.Millisecond {
			t.Errorf("caller %d waited %s against its own 400ms bound", i, d)
		}
	}
}

// Reload rewrites spec under p.mu while requests read the plugin unlocked. Run
// with -race: the detector is the assertion.
func TestHold_ReloadRacesNoRequest(t *testing.T) {
	app := holdHost(t, 1, lazy(t, "demo0", time.Hour))
	if c, _ := get(t, app, "/v1/demo0/version"); c != 200 {
		t.Fatalf("demo0: %d", c)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = get(t, app, "/v1/demo0/version")
				app.Evict()
			}
		}()
	}
	for range 3 {
		if err := app.Reload("demo0", Plugin{}); err != nil {
			t.Fatalf("reload: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}
