package zip_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"

	"github.com/zap-proto/zip"
)

// The app under test: a plugin with typed ops, exactly as a real one is
// written. Nothing here knows it will be called over a socket.

type boolIn struct {
	Flag string `json:"flag" validate:"required"`
	Org  string `json:"org"`
}
type boolOut struct {
	Flag    string `json:"flag"`
	Enabled bool   `json:"enabled"`
	// Caller is what the callee observed about who called it — the gateway's
	// assertion, forwarded, not anything the client made up.
	Caller string `json:"caller"`
	// PeerUID is the kernel's attestation of the calling process. Reachable
	// only over a unix socket, which is what makes it proof of the transport.
	PeerUID int `json:"peer_uid"`
	PeerPID int `json:"peer_pid"`
}

type retireIn struct {
	Flag string `json:"flag" validate:"required"`
}

// flagsApp is a zip plugin serving two typed ops. It is the whole "server side"
// of these tests — no MCP code, no client code, no schema.
func flagsApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "flags", DisableStartupMessage: true})
	zip.Post(app, "/v1/flags/bool", func(ctx context.Context, in *boolIn) (*boolOut, error) {
		if in.Flag == "explode" {
			return nil, zip.ErrForbidden("flag is sealed")
		}
		out := &boolOut{Flag: in.Flag, Enabled: in.Flag == "beta"}
		out.Caller = zip.HeaderOrg + "=" + zip.CallerOf(ctx).Org
		if p := zip.PeerOf(ctx); p != nil {
			out.PeerUID, out.PeerPID = p.UID, p.PID
		}
		return out, nil
	}, zip.WithOperationID("flags_bool"), zip.WithSummary("Evaluate one boolean flag"))

	// A void op: the handler returns a nil *Out, which must survive the crossing
	// as a nil *Out and a nil error rather than becoming a decode failure.
	zip.Post(app, "/v1/flags/retire", func(_ context.Context, _ *retireIn) (*boolOut, error) {
		return nil, nil
	}, zip.WithOperationID("flags_retire"), zip.WithSummary("Retire a flag"))
	return app
}

// serveUDS starts app on a unix socket under a private temp dir and returns the
// socket path. Only ONE address is served and its scheme is the default (ZAP) —
// there is no HTTP listener in this process for a call to fall back to.
func serveUDS(t *testing.T, app *zip.App) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "flags.sock")
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitSock(t, sock)
	return sock
}

func waitSock(t *testing.T, sock string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never began listening", sock)
}

// TestCall_TypedRoundTripOverZAPOnUnixSocket is the end-to-end proof of the
// op-call plane: a real zip plugin listens on a unix domain socket, a client
// dials THAT socket, and a typed op runs — request in, typed response out —
// with no HTTP anywhere in the path and no Go import of the plugin's package
// beyond the types the call is parameterised on.
//
// The path is pure Go: net.Dial + zap-proto/go framing, no cgo resolver and no
// C dependency, so CGO_ENABLED=0 changes nothing. The suite is run both ways.
func TestCall_TypedRoundTripOverZAPOnUnixSocket(t *testing.T) {
	sock := serveUDS(t, flagsApp(t))

	c, err := zip.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.Addr() != sock {
		t.Fatalf("Conn dials %q, want the socket %q", c.Addr(), sock)
	}

	out, err := zip.Call[boolIn, boolOut](context.Background(), c, "flags_bool", &boolIn{Flag: "beta"})
	if err != nil {
		t.Fatalf("call flags_bool: %v", err)
	}
	if out == nil || out.Flag != "beta" || !out.Enabled {
		t.Fatalf("typed round trip wrong: %+v", out)
	}

	// The kernel attested a peer credential. That is only possible over a unix
	// socket — peerOf returns nil for anything else — so this asserts the dial
	// really was "unix" and not a loopback tcp fallback.
	if out.PeerUID != os.Getuid() {
		t.Fatalf("peer uid = %d, want this process's uid %d (call did not arrive over a unix socket)",
			out.PeerUID, os.Getuid())
	}
	if out.PeerPID != os.Getpid() {
		t.Fatalf("peer pid = %d, want %d", out.PeerPID, os.Getpid())
	}

	// A void op crosses as a void result, not as a decode error.
	none, err := zip.Call[retireIn, boolOut](context.Background(), c, "flags_retire", &retireIn{Flag: "old"})
	if err != nil {
		t.Fatalf("call flags_retire: %v", err)
	}
	if none != nil {
		t.Fatalf("void op returned %+v, want nil", none)
	}

	// The handler's error arrives as the very HTTPError it returned — status and
	// message intact — so the caller reacts to what the callee raised.
	if _, err := zip.Call[boolIn, boolOut](context.Background(), c, "flags_bool", &boolIn{Flag: "explode"}); err == nil {
		t.Fatal("sealed flag: want an error")
	} else {
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != 403 || he.Msg != "flag is sealed" {
			t.Fatalf("remote error did not survive the crossing: %#v", err)
		}
	}

	// Validation runs on the callee, on the decoded input, exactly as it does
	// for the REST projection.
	if _, err := zip.Call[boolIn, boolOut](context.Background(), c, "flags_bool", &boolIn{}); err == nil {
		t.Fatal("missing required flag: want a 400")
	} else {
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != 400 {
			t.Fatalf("want 400 from the callee's validator, got %#v", err)
		}
	}

	// An unknown op is a clean 404 rather than a hang or a mystery route.
	if _, err := zip.Call[boolIn, boolOut](context.Background(), c, "nope", &boolIn{Flag: "x"}); err == nil {
		t.Fatal("unknown op: want an error")
	} else {
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != 404 {
			t.Fatalf("want 404 for an unknown op, got %#v", err)
		}
	}
}

// TestCall_WireIsZAPFramesNotHTTP proves the bytes on that socket are ZAP
// frames. It writes a length-prefixed ZAP request frame with zaphttp's own
// codec and decodes the reply with it — a positive identification of the wire
// format, not an inference from the fact that a call succeeded.
//
// It then shows the negative: a well-formed HTTP/1.1 request gets no HTTP
// response, because nothing on this socket speaks HTTP.
func TestCall_WireIsZAPFramesNotHTTP(t *testing.T) {
	sock := serveUDS(t, flagsApp(t))

	// --- positive: a hand-built ZAP frame is understood -----------------------
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.SetHost("flags")
	req.URI().SetPath(zip.CallPath + "flags_bool")
	req.SetBodyString(`{"flag":"beta"}`)

	frame, err := zaphttp.MarshalRequest(req)
	if err != nil {
		t.Fatalf("marshal ZAP request frame: %v", err)
	}

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial unix: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(frame)))
	if _, err := conn.Write(append(hdr[:], frame...)); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	if _, err := readFull(br, hdr[:]); err != nil {
		t.Fatalf("read response length prefix: %v", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > zaphttp.MaxFrameSize {
		t.Fatalf("implausible frame length %d — these are not ZAP frames", n)
	}
	respFrame := make([]byte, n)
	if _, err := readFull(br, respFrame); err != nil {
		t.Fatalf("read response frame: %v", err)
	}

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	if err := zaphttp.UnmarshalResponse(respFrame, resp); err != nil {
		t.Fatalf("the reply is not a ZAP response frame: %v", err)
	}
	if resp.StatusCode() != 200 || !strings.Contains(string(resp.Body()), `"enabled":true`) {
		t.Fatalf("ZAP frame carried the wrong reply: %d %s", resp.StatusCode(), resp.Body())
	}

	// --- negative: HTTP text gets no HTTP answer ------------------------------
	hc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial unix: %v", err)
	}
	defer func() { _ = hc.Close() }()
	if _, err := hc.Write([]byte("GET /.well-known/openapi.json HTTP/1.1\r\nHost: flags\r\n\r\n")); err != nil {
		t.Fatalf("write HTTP request: %v", err)
	}
	_ = hc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	got, _ := hc.Read(buf)
	if got > 0 && strings.HasPrefix(string(buf[:got]), "HTTP/1.") {
		t.Fatalf("the socket answered HTTP text (%q) — this transport is not ZAP-native", buf[:got])
	}
}

func readFull(r *bufio.Reader, p []byte) (int, error) {
	n := 0
	for n < len(p) {
		m, err := r.Read(p[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// TestCall_ForwardsIdentityWithoutMintingIt proves the two identity rules at
// once: a call made from inside a request carries that request's gateway
// headers onward, and a call made from a bare context carries nothing. The
// client never invents an identity — it only propagates one it was given.
func TestCall_ForwardsIdentityWithoutMintingIt(t *testing.T) {
	sock := serveUDS(t, flagsApp(t))
	c, err := zip.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// A background context has no request behind it, so nothing is forwarded.
	out, err := zip.Call[boolIn, boolOut](context.Background(), c, "flags_bool", &boolIn{Flag: "beta"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := strings.TrimPrefix(out.Caller, zip.HeaderOrg+"="); got != "" {
		t.Fatalf("an unattributed call forwarded org %q — the client must not invent identity", got)
	}

	// An aggregator: an untyped handler that calls the plugin with c.Forward().
	agg := zip.New(zip.Config{AppName: "aggregator", DisableStartupMessage: true})
	agg.Post("/v1/agg/eval", func(rc *zip.Ctx) error {
		o, cerr := zip.Call[boolIn, boolOut](rc.Forward(), c, "flags_bool", &boolIn{Flag: "beta"})
		if cerr != nil {
			return cerr
		}
		return rc.JSON(200, o)
	})
	aggSock := serveUDS(t, agg)

	ac, err := zip.Dial(aggSock)
	if err != nil {
		t.Fatalf("dial aggregator: %v", err)
	}
	defer func() { _ = ac.Close() }()

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.SetHost("aggregator")
	req.URI().SetPath("/v1/agg/eval")
	req.Header.Set(zip.HeaderOrg, "acme")
	req.SetBodyString("{}")
	if err := zaphttp.Dial("unix", aggSock).Do(req, resp); err != nil {
		t.Fatalf("call aggregator: %v", err)
	}
	if !strings.Contains(string(resp.Body()), `"caller":"`+zip.HeaderOrg+`=acme"`) {
		t.Fatalf("the aggregator did not forward the gateway's org: %s", resp.Body())
	}
}

// TestSocketPath_IsTheOneScheme pins the ONE canonical name→socket mapping that
// both halves use: a service serves at zip.SocketPath(name) and a caller
// reaches it with zip.DialApp(name). If these two ever disagree, every call in
// the fleet misses.
func TestSocketPath_IsTheOneScheme(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(zip.RuntimeDirEnv, dir)

	if got, want := zip.RuntimeDir(), dir; got != want {
		t.Fatalf("RuntimeDir() = %q, want %q", got, want)
	}
	if got, want := zip.SocketPath("flags"), filepath.Join(dir, "flags.sock"); got != want {
		t.Fatalf("SocketPath(\"flags\") = %q, want %q", got, want)
	}

	// The scheme resolves for real: serve where SocketPath says, dial by name.
	app := flagsApp(t)
	go func() { _ = app.Listen(zip.Addr(zip.SocketPath("flags"))) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitSock(t, zip.SocketPath("flags"))

	c, err := zip.DialApp("flags")
	if err != nil {
		t.Fatalf("DialApp: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.Addr() != zip.SocketPath("flags") {
		t.Fatalf("DialApp resolved %q, want %q", c.Addr(), zip.SocketPath("flags"))
	}
	out, err := zip.Call[boolIn, boolOut](context.Background(), c, "flags_bool", &boolIn{Flag: "beta"})
	if err != nil || out == nil || !out.Enabled {
		t.Fatalf("call by name: out=%+v err=%v", out, err)
	}
}

// TestRuntimeDir_ResolutionOrder pins the one resolution order documented on
// RuntimeDir, including the per-user fallback that makes a dev box work with no
// configuration at all.
func TestRuntimeDir_ResolutionOrder(t *testing.T) {
	t.Setenv(zip.RuntimeDirEnv, "/explicit")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := zip.RuntimeDir(); got != "/explicit" {
		t.Fatalf("ZIP_RUNTIME_DIR must win: got %q", got)
	}

	t.Setenv(zip.RuntimeDirEnv, "")
	if got, want := zip.RuntimeDir(), filepath.Join("/run/user/1000", "zip"); got != want {
		t.Fatalf("XDG fallback: got %q, want %q", got, want)
	}

	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := zip.RuntimeDir(); got != "/run/zip" {
		t.Fatalf("system default: got %q, want /run/zip", got)
	}
}

// TestCall_RejectsAnOpNameThatIsNotAnOp keeps a caller bug from becoming a
// request to some other route.
func TestCall_RejectsAnOpNameThatIsNotAnOp(t *testing.T) {
	c, err := zip.Dial(filepath.Join(t.TempDir(), "unused.sock"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	for _, bad := range []string{"", "../../etc/passwd", "flags_bool?x=1", "a b"} {
		if _, err := zip.Call[boolIn, boolOut](context.Background(), c, bad, &boolIn{Flag: "x"}); err == nil {
			t.Fatalf("op name %q was accepted", bad)
		}
	}
}

// TestCall_CancelledContextNeverReachesTheWire — an already-cancelled ctx fails
// before a connection is made, so a caller that gave up does not cost the
// callee a request.
func TestCall_CancelledContextNeverReachesTheWire(t *testing.T) {
	sock := serveUDS(t, flagsApp(t))
	c, err := zip.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := zip.Call[boolIn, boolOut](ctx, c, "flags_bool", &boolIn{Flag: "beta"}); err == nil {
		t.Fatal("a cancelled context must not reach the wire")
	}
}
