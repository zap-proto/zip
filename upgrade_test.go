package zip_test

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// A MOUNTED PLUGIN'S WEBSOCKET REACHES THE CALLER.
//
// The plugin's own socket carries ZAP frames — one request, one reply — so an
// upgraded connection cannot cross it: the host wrote 101 and then went on
// parsing HTTP, and the first frame the client sent (0x81…) arrived at the
// header parser as garbage. Every websocket behind a plugin died on its first
// message. These tests pin the two halves of the fix: the switch is relayed and
// the bytes flow both ways, and a plugin that REFUSES to switch still answers.

// upstream stands in for the plugin's plain-HTTP leg: it speaks HTTP/1.1 on the
// sibling socket, switches protocols, and then echoes what it is sent in caps.
func upstream(t *testing.T, sock string, code, body string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				if code != "101" {
					_, _ = c.Write([]byte("HTTP/1.1 " + code + " No\r\nContent-Length: " +
						itoa(len(body)) + "\r\n\r\n" + body))
					return
				}
				_, _ = c.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
					"Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
				for {
					b := make([]byte, 256)
					n, err := br.Read(b)
					if err != nil {
						return
					}
					if _, err := c.Write([]byte(strings.ToUpper(string(b[:n])))); err != nil {
						return
					}
				}
			}()
		}
	}()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

// mounted starts a ZAP plugin plus its plain-HTTP sibling, mounts it on a host
// listening over real TCP, and answers the host's address.
func mounted(t *testing.T, code, body string) string {
	t.Helper()
	dir := sockDir(t)
	sock := dir + "/box.sock"

	child := zip.New(zip.Config{AppName: "box", DisableStartupMessage: true})
	child.Get("/v1/box/ping", func(c *zip.Ctx) error { return c.String(200, "ok") })
	go func() { _ = child.Listen(sock) }()
	waitSock(t, sock)
	upstream(t, sock+".http", code, body)

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
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = host.Shutdown() })
	return addr
}

func TestMountedPluginRelaysAnUpgrade(t *testing.T) {
	addr := mounted(t, "101", "")

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte("GET /v1/box/ws HTTP/1.1\r\nHost: x\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("want 101, got %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read head: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	// PAST THE SWITCH: this is the byte that used to reach an HTTP parser.
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := make([]byte, 5)
	if _, err := br.Read(got); err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(got) != "HELLO" {
		t.Fatalf("want HELLO, got %q", got)
	}
}

func TestMountedPluginMayRefuseToUpgrade(t *testing.T) {
	addr := mounted(t, "401", `{"error":"no ticket"}`)

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte("GET /v1/box/ws HTTP/1.1\r\nHost: x\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	br := bufio.NewReader(c)
	head, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(head, "401") {
		t.Fatalf("want 401, got %q", head)
	}
	rest := make([]byte, 512)
	n, _ := br.Read(rest)
	if !strings.Contains(string(rest[:n]), "no ticket") {
		t.Fatalf("want the plugin's own body, got %q", rest[:n])
	}
}
