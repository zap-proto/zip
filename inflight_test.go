package zip_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// A process answering a request is never the one stopped for room, and recency is
// when a request ENDED. Each test here puts a request in flight on demo0, makes
// the ceiling ask for room, and reads which process went — the arrangement that
// stopped a subsystem mid-answer in production and read as a 502.

// fetchAsync is call for a goroutine: it reports instead of failing the test.
func fetchAsync(app *zip.App, target string) (int, string, error) {
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: deadline, FailOnTimeout: true})
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

type answer struct {
	status int
	body   string
	err    error
	took   time.Duration
}

// inflight starts a request in the background and returns once its plugin is
// running, so the request is being served when the caller acts.
func inflight(t *testing.T, app *zip.App, name, target string) <-chan answer {
	t.Helper()
	out := make(chan answer, 1)
	go func() {
		start := time.Now()
		status, body, err := fetchAsync(app, target)
		out <- answer{status, body, err, time.Since(start)}
	}()
	if !settles(5*time.Second, func() bool { return pidOf(app, name) != 0 }) {
		t.Fatalf("%s never started", name)
	}
	time.Sleep(50 * time.Millisecond) // the plugin is up; let the request reach it
	return out
}

func pidOf(app *zip.App, name string) int {
	for _, p := range app.Plugins() {
		if p.Name == name && p.Running {
			return p.PID
		}
	}
	return 0
}

func completed(t *testing.T, a answer, want string) {
	t.Helper()
	if a.err != nil || a.status != 200 || !strings.Contains(a.body, want) {
		t.Fatalf("the request in flight did not complete: status=%d err=%v body=%q",
			a.status, a.err, a.body)
	}
}

// TestInflight_ALongRequestKeepsItsProcess is the production failure: demo0 is
// asked first and is still answering when the ceiling needs room. By start time
// demo0 is the coldest; it is also the only one busy. demo1 must go.
func TestInflight_ALongRequestKeepsItsProcess(t *testing.T) {
	app := warmHost(t, 3, 2, time.Hour)

	slow := inflight(t, app, "demo0", "/v1/demo0/version?sleep=1500ms")
	pid := pidOf(app, "demo0")
	if status, _ := call(t, app, "GET", "/v1/demo1/version", ""); status != 200 {
		t.Fatalf("demo1 did not serve: %d", status)
	}
	if status, _ := call(t, app, "GET", "/v1/demo2/version", ""); status != 200 {
		t.Fatalf("demo2 did not serve: %d", status)
	}

	completed(t, <-slow, `"echo":"/v1/demo0/version"`)
	if got := pidOf(app, "demo0"); got != pid {
		t.Fatalf("demo0 was stopped mid-request: pid %d, now %d", pid, got)
	}
	if pidOf(app, "demo1") != 0 {
		t.Fatal("demo1 — the idle one — is still running; something else made the room")
	}
}

// TestInflight_AStreamKeepsItsProcess holds the same line for a reply that is
// still streaming: the request is in flight until the body ends, not until the
// head arrives.
func TestInflight_AStreamKeepsItsProcess(t *testing.T) {
	app := warmHost(t, 3, 2, time.Hour)

	stream := inflight(t, app, "demo0", "/v1/demo0/feed?drip=1500ms")
	pid := pidOf(app, "demo0")
	for _, name := range []string{"demo1", "demo2"} {
		if status, _ := call(t, app, "GET", "/v1/"+name+"/version", ""); status != 200 {
			t.Fatalf("%s did not serve: %d", name, status)
		}
	}

	a := <-stream
	completed(t, a, "tick\n")
	if !strings.HasSuffix(a.body, "done\n") {
		t.Fatalf("the stream was cut before its end: %q", a.body)
	}
	if got := pidOf(app, "demo0"); got != pid {
		t.Fatalf("demo0 was stopped mid-stream: pid %d, now %d", pid, got)
	}
}

// TestInflight_EveryoneBusyWaits is the case with no idle candidate at all. The
// starter waits for a request to finish and takes that room; neither request in
// flight is killed to make it.
func TestInflight_EveryoneBusyWaits(t *testing.T) {
	app := warmHost(t, 3, 2, time.Hour)

	first := inflight(t, app, "demo0", "/v1/demo0/version?sleep=1200ms")
	second := inflight(t, app, "demo1", "/v1/demo1/version?sleep=1200ms")

	start := time.Now()
	status, body := call(t, app, "GET", "/v1/demo2/version", "")
	waited := time.Since(start)
	if status != 200 {
		t.Fatalf("demo2 did not serve after waiting: %d %q", status, body)
	}
	completed(t, <-first, `"echo":"/v1/demo0/version"`)
	completed(t, <-second, `"echo":"/v1/demo1/version"`)
	if waited < 500*time.Millisecond {
		t.Fatalf("demo2 started after %s, before any request in flight finished — "+
			"someone busy was stopped for it", waited)
	}
	if live := len(runningPIDs(t, app)); live > 2 {
		t.Fatalf("%d processes against a ceiling of 2", live)
	}
}

// TestInflight_NoRoomIsA503 bounds the wait: a starter waits as long as its own
// start may take, then is refused. The request in flight still completes.
func TestInflight_NoRoomIsA503(t *testing.T) {
	bin := buildPlugin(t, "v1")
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true, Warm: 1})
	for _, p := range []zip.Plugin{
		{Name: "demo0", Bin: bin, Dir: sockDir(t), Lazy: true, IdleAfter: time.Hour},
		{Name: "demo1", Bin: bin, Dir: sockDir(t), Lazy: true, IdleAfter: time.Hour, Start: 300 * time.Millisecond},
	} {
		app.Use(must(zip.Load(p, "/v1/"+p.Name)))
	}
	t.Cleanup(func() { _ = app.Shutdown() })

	slow := inflight(t, app, "demo0", "/v1/demo0/version?sleep=2s")
	start := time.Now()
	status, _ := call(t, app, "GET", "/v1/demo1/version", "")
	if status != 503 {
		t.Fatalf("status %d, want 503 while the only process is busy", status)
	}
	if took := time.Since(start); took < 250*time.Millisecond {
		t.Fatalf("refused after %s, without waiting for room", took)
	}
	completed(t, <-slow, `"echo":"/v1/demo0/version"`)
}

// TestInflight_RecencyIsWhenARequestEnded is the ranking. demo0 is asked first
// and answers last; demo1 is asked after it and answers at once. By start time
// demo0 is the coldest. By end time it is the warmest, and demo1 goes.
func TestInflight_RecencyIsWhenARequestEnded(t *testing.T) {
	app := warmHost(t, 3, 2, time.Hour)

	slow := inflight(t, app, "demo0", "/v1/demo0/version?sleep=800ms")
	if status, _ := call(t, app, "GET", "/v1/demo1/version", ""); status != 200 {
		t.Fatal("demo1 did not serve")
	}
	completed(t, <-slow, `"echo":"/v1/demo0/version"`)
	pid := pidOf(app, "demo0")

	if status, _ := call(t, app, "GET", "/v1/demo2/version", ""); status != 200 {
		t.Fatal("demo2 did not serve")
	}
	if got := pidOf(app, "demo0"); got != pid {
		t.Fatal("demo0 was evicted, ranked by when its request STARTED")
	}
	if pidOf(app, "demo1") != 0 {
		t.Fatal("demo1, the least recently active, is still running")
	}
}

// TestInflight_TheSweepSparesTheBusy is the age bound's half: a process older than
// its IdleAfter is not stopped while it is answering.
func TestInflight_TheSweepSparesTheBusy(t *testing.T) {
	app := warmHost(t, 1, 0, 200*time.Millisecond)

	slow := inflight(t, app, "demo0", "/v1/demo0/version?sleep=3s") // outlasts the four sweeps
	pid := pidOf(app, "demo0")
	for range 4 {
		time.Sleep(250 * time.Millisecond)
		if n := app.Evict(); n != 0 {
			t.Fatalf("Evict stopped %d processes while demo0 was answering", n)
		}
	}
	completed(t, <-slow, `"echo":"/v1/demo0/version"`)
	if got := pidOf(app, "demo0"); got != pid {
		t.Fatalf("demo0 changed under its request: %d → %d", pid, got)
	}
}
