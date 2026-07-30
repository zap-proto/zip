package zip_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The ops surface is a SECOND listener. HIP-0119 §1 wants /metrics off the
// public port, and a probe that queues behind public traffic is not a probe.
func TestOpsSurfaceIsASecondListener(t *testing.T) {
	port := freePort(t)
	t.Setenv(zip.OpsAddrEnv, "http://127.0.0.1:"+port)
	t.Setenv(zip.RuntimeDirEnv, t.TempDir())

	app := zip.New(zip.Config{AppName: "opsy", DisableStartupMessage: true})
	zip.Get(app, "/v1/opsy/thing", func(ctx context.Context, in *askIn) (*askOut, error) {
		return &askOut{Org: "x"}, nil
	}, zip.WithOperationID("opsy_thing"))

	pub := freePort(t)
	go func() { _ = app.Listen("http://127.0.0.1:" + pub) }()
	t.Cleanup(func() { _ = app.Shutdown(); _ = app.Ops().Shutdown() })
	waitHTTP(t, "http://127.0.0.1:"+port+zip.HealthPath)

	for _, p := range []string{zip.HealthPath, zip.ReadyPath, zip.MetricsPath} {
		if code, _ := get(t, "http://127.0.0.1:"+port+p); code != 200 {
			t.Fatalf("ops %s = %d, want 200", p, code)
		}
		// The same path on the PUBLIC listener must not answer.
		if code, _ := get(t, "http://127.0.0.1:"+pub+p); code == 200 {
			t.Fatalf("public listener served %s — /metrics is not a public door", p)
		}
	}
	// And the ops listener serves none of the app's own routes.
	if code, _ := get(t, "http://127.0.0.1:"+port+"/v1/opsy/thing"); code == 200 {
		t.Fatal("the ops listener served a product route")
	}

	// The metrics are read off live structures, so they carry this app's real
	// registry and route counts rather than a placeholder.
	_, body := get(t, "http://127.0.0.1:"+port+zip.MetricsPath)
	for _, want := range []string{"zip_up 1", "zip_ops 1", "zip_uptime_seconds", "zip_routes"} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

// There is NO default ops port, and that is the point. A default meant every
// process calling Listen tried to bind one port — and a plugin has two apps in
// it while a test process has many, so the second died on "address already in
// use". A host sets no OPS_PORT on a child, so a child owns none.
func TestThereIsNoDefaultOpsPort(t *testing.T) {
	t.Setenv(zip.OpsAddrEnv, "")
	t.Setenv(zip.OpsPortEnv, "")
	if got := zip.OpsAddr(); got != "" {
		t.Fatalf("OpsAddr = %q with nothing configured — a surprise bind", got)
	}
	t.Setenv(zip.OpsPortEnv, zip.DefaultOpsPort)
	if got := zip.OpsAddr(); got != ":9090" {
		t.Fatalf("OPS_PORT ignored: %q", got)
	}
}

// Two apps in one process — the edge app and its peer sibling — must not race
// for one ops port. Only the primary brings the surface up.
func TestOnlyThePrimaryAppOwnsTheOpsListener(t *testing.T) {
	t.Setenv(zip.RuntimeDirEnv, t.TempDir())
	t.Setenv(zip.OpsPortEnv, freePort(t))

	app := zip.New(zip.Config{AppName: "twoapps", DisableStartupMessage: true})
	zip.Post(app.Peer(), "/v1/twoapps/peer", func(ctx context.Context, in *askIn) (*askOut, error) {
		return &askOut{Org: "peer"}, nil
	}, zip.WithOperationID("twoapps_peer"))
	zip.Get(app, "/v1/twoapps/edge", func(ctx context.Context, in *askIn) (*askOut, error) {
		return &askOut{Org: "edge"}, nil
	}, zip.WithOperationID("twoapps_edge"))

	pub := freePort(t)
	go func() { _ = app.Listen("http://127.0.0.1:" + pub) }()
	t.Cleanup(func() { _ = app.Shutdown(); _ = app.Ops().Shutdown(); _ = app.Peer().Shutdown() })
	waitHTTP(t, "http://127.0.0.1:"+pub+"/v1/twoapps/edge?ref=x")

	// One verb brought up three listeners: edge, peer socket, ops port.
	waitSock(t, zip.SocketPath("twoapps"))
	ctx := zip.WithCaller(context.Background(), zip.Caller{User: "u", Org: "o"})
	out, err := zip.Ask[askIn, askOut](ctx, "twoapps", "twoapps_peer", &askIn{})
	if err != nil || out.Org != "peer" {
		t.Fatalf("Ask over the peer socket: %v %+v", err, out)
	}
	// The peer op is NOT on the edge: a typed op rides every transport its app
	// listens on, which is why the separation has to be two apps.
	if code, _ := get(t, "http://127.0.0.1:"+pub+"/v1/twoapps/peer"); code == 200 {
		t.Fatal("an internal op answered on the public listener")
	}
	if _, err := zip.Ask[askIn, askOut](ctx, "twoapps", "twoapps_edge", &askIn{}); err == nil {
		t.Fatal("an edge op answered on the peer socket")
	}
}

// The ops paths are control plane, so they never appear in a declaration — a
// plugin declaring /metrics would claim it for the whole composition.
func TestOpsPathsAreNotDeclared(t *testing.T) {
	app := zip.New(zip.Config{AppName: "d", DisableStartupMessage: true})
	app.Get("/v1/d/x", func(c *zip.Ctx) error { return nil })
	for _, r := range app.Ops().Declaration().Routes {
		if r.Pattern == zip.MetricsPath || r.Pattern == zip.HealthPath || r.Pattern == zip.ReadyPath {
			t.Fatalf("the ops app declared its own control plane: %v", r)
		}
	}
	for _, r := range app.Declaration().Routes {
		if r.Pattern == zip.MetricsPath {
			t.Fatalf("a product app declared /metrics: %v", r)
		}
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
