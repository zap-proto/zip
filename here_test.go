package zip_test

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

type addIn struct {
	A int `json:"a"`
	B int `json:"b"`
}

type addOut struct {
	Sum int `json:"sum"`
}

// A co-resident op is reached through the OP, not through the wire.
//
// This is the property the seam exists for, and the test proves it the only way
// that cannot be argued with: it takes the wire away. The listener is shut down,
// a dial to the socket then fails — and the same op, on the same app, still
// answers. A call that needed the socket could not survive that.
//
// It matters because the alternative shape has a cost that is not obvious.
// cloud reached its own co-resident subsystems by building an http.Request and
// dispatching it into its own router, which re-ran every edge middleware —
// including middlewares that themselves read the subsystem being served, so a
// read re-entered the whole app without bound. The cure there was a depth
// counter keyed on a goroutine id parsed out of runtime.Stack. The cure here is
// not calling the router at all: [Here] enters one op.
func TestHereRunsTheOpWithoutTheSocket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(zip.RuntimeDirEnv, dir)

	var ran atomic.Int64
	app := zip.New(zip.Config{AppName: "calc"})
	zip.Post[addIn, addOut](app, "/add", func(_ context.Context, in *addIn) (*addOut, error) {
		ran.Add(1)
		return &addOut{Sum: in.A + in.B}, nil
	}, zip.WithOperationID("add"))

	sock := zip.SocketPath("calc")
	if want := filepath.Join(dir, "calc.sock"); sock != want {
		t.Fatalf("socket path = %s, want %s", sock, want)
	}
	go func() { _ = app.Listen(sock) }()
	waitFor(t, sock)

	// The app answers for its own name, in this process.
	a := zip.Serving("calc")
	if a == nil {
		t.Fatal("Serving(calc) = nil after Listen on its canonical socket")
	}
	if a != app {
		t.Fatal("Serving(calc) returned a different app than the one that bound the socket")
	}

	// Over the socket: a real connection, a real encode, for comparison.
	conn, err := zip.DialApp("calc")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	remote, err := zip.Call[addIn, addOut](context.Background(), conn, "add", &addIn{A: 2, B: 3})
	if err != nil {
		t.Fatalf("call over the socket: %v", err)
	}
	if remote.Sum != 5 {
		t.Fatalf("socket call = %d, want 5", remote.Sum)
	}
	_ = conn.Close()

	// In-process, with the listener still up: same op, same answer.
	local, err := zip.Here[addIn, addOut](context.Background(), a, "add", &addIn{A: 2, B: 3})
	if err != nil {
		t.Fatalf("Here: %v", err)
	}
	if local.Sum != 5 {
		t.Fatalf("Here = %d, want 5", local.Sum)
	}

	// Now take the wire away.
	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the socket still accepts after Shutdown; the wire was not actually removed")
	}
	after, err := zip.Here[addIn, addOut](context.Background(), app, "add", &addIn{A: 2, B: 3})
	if err != nil {
		t.Fatalf("Here with no listener: %v — the in-process call is still going through the wire", err)
	}
	if after.Sum != 5 {
		t.Fatalf("Here with no listener = %d, want 5", after.Sum)
	}
	if got := ran.Load(); got != 3 {
		t.Fatalf("handler ran %d times, want 3 (socket, in-process, in-process with no listener)", got)
	}
}

// An op this app does not answer is the same refusal whichever way it is
// reached — a name is a name, not a transport.
func TestHereRefusesAnUnknownOp(t *testing.T) {
	app := zip.New(zip.Config{AppName: "calc"})
	zip.Post[addIn, addOut](app, "/add", func(_ context.Context, in *addIn) (*addOut, error) {
		return &addOut{Sum: in.A + in.B}, nil
	}, zip.WithOperationID("add"))

	if _, err := zip.Here[addIn, addOut](context.Background(), app, "subtract", &addIn{}); err == nil {
		t.Fatal("Here on an unregistered op returned no error")
	}
	if _, err := zip.Here[addIn, addOut](context.Background(), nil, "add", &addIn{}); err == nil {
		t.Fatal("Here with no app returned no error")
	}
}

// Serving answers about THIS process and nothing else. An app that never bound
// its socket is not here, and one that shut down stops being here — otherwise a
// caller would be handed an app on its way out while the socket beside it was
// already refusing connections.
func TestServingFollowsTheListener(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(zip.RuntimeDirEnv, dir)

	app := zip.New(zip.Config{AppName: "meter"})
	zip.Post[addIn, addOut](app, "/add", func(_ context.Context, in *addIn) (*addOut, error) {
		return &addOut{Sum: in.A + in.B}, nil
	}, zip.WithOperationID("add"))

	if zip.Serving("meter") != nil {
		t.Fatal("Serving(meter) is non-nil before anything bound the socket")
	}
	sock := zip.SocketPath("meter")
	go func() { _ = app.Listen(sock) }()
	waitFor(t, sock)
	if zip.Serving("meter") == nil {
		t.Fatal("Serving(meter) = nil while its socket is bound")
	}
	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if zip.Serving("meter") != nil {
		t.Fatal("Serving(meter) is still answering after Shutdown")
	}
}

// The direct seam runs the SAME contract the decoding one does: an input the op
// declared invalid is refused identically whether it arrived as bytes or as a
// value. A second path into a handler that skipped a check would be a hole in
// exactly the shape nobody looks at.
func TestHereValidatesLikeTheWire(t *testing.T) {
	type payIn struct {
		Subject string `json:"subject" validate:"required"`
	}
	app := zip.New(zip.Config{AppName: "till"})
	zip.Post[payIn, addOut](app, "/pay", func(_ context.Context, _ *payIn) (*addOut, error) {
		return &addOut{Sum: 1}, nil
	}, zip.WithOperationID("pay"))

	if _, err := zip.Here[payIn, addOut](context.Background(), app, "pay", &payIn{}); err == nil {
		t.Fatal("Here accepted an input the op's own validation rejects")
	}
	if _, err := zip.Here[payIn, addOut](context.Background(), app, "pay", &payIn{Subject: "acme"}); err != nil {
		t.Fatalf("Here refused a valid input: %v", err)
	}
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s never accepted", path)
}
