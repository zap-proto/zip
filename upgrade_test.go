package zip_test

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"
)

// A MOUNTED PLUGIN'S WEBSOCKET REACHES THE CALLER.
//
// A plugin's socket carries ZAP frames — one request, one reply — and an
// upgraded connection is neither. The host wrote the 101 through and then went
// on parsing HTTP, so the first frame the client sent arrived at the header
// parser as nonsense (first byte 0x81) and the connection died. Every websocket
// behind a mounted plugin failed on its first message, and the session the
// plugin meant to run after the handshake never started.
//
// These pin both halves: the switch is relayed and the bytes flow both ways,
// and a plugin that REFUSES to switch still answers its own reply.

// box is a plugin in the shape production runs: ONE address, the way a plugin
// main is written. The plain-HTTP sibling an upgrade needs is derived from that
// address, not passed alongside it — so this also pins that Listen creates it.
// Naming the sibling here instead would test the relay while assuming away the
// wiring, and a mounted websocket would still fail everywhere it is really used.
func box(t *testing.T) string {
	t.Helper()
	dir := sockDir(t)
	sock := dir + "/box.sock"

	child := zip.New(zip.Config{AppName: "box", DisableStartupMessage: true})
	child.Get("/v1/box/ws", wsx.Upgrade(func(c *wsx.Conn) error {
		for {
			typ, msg, err := c.ReadMessage()
			if err != nil {
				return err
			}
			if err := c.WriteMessage(typ, bytes.ToUpper(msg)); err != nil {
				return err
			}
		}
	}))
	// Refused BEFORE the upgrade, which is how an unticketed terminal is refused.
	child.Get("/v1/box/shut", func(c *zip.Ctx) error {
		return zip.ErrUnauthorized("no ticket")
	})
	go func() { _ = child.Listen(sock) }()
	waitSock(t, sock)
	waitSock(t, sock+".http")
	t.Cleanup(func() { _ = child.Shutdown() })

	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	loaded, err := zip.Load(zip.Plugin{Name: "box", Addr: sock}, "/v1/box")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	host.Use(loaded)

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	go func() { _ = host.Listen("http://" + addr) }()
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = host.Shutdown() })
	return addr
}

func TestMountedPluginServesAWebsocket(t *testing.T) {
	addr := box(t)

	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := d.Dial("ws://"+addr+"/v1/box/ws", nil)
	if err != nil {
		t.Fatalf("dial through the mount: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// PAST THE SWITCH: the frame that used to reach an HTTP header parser.
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, got, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(got) != "HELLO" {
		t.Fatalf("want HELLO, got %q", got)
	}
}

func TestMountedPluginMayRefuseToUpgrade(t *testing.T) {
	addr := box(t)

	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	_, resp, err := d.Dial("ws://"+addr+"/v1/box/shut", nil)
	if err == nil {
		t.Fatal("want the plugin's refusal, got a socket")
	}
	if resp == nil {
		t.Fatalf("want a reply to read, got only %v", err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}
