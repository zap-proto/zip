// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/websocket"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"
)

// An op whose answer is a WebSocket validates, authorizes and binds its input
// like any other, and then the connection is upgraded and served. Its document
// shows the upgrade and the type of one message; it is no MCP tool, and
// tools/list says why.

type roomIn struct {
	Room string `json:"room" url:"room" validate:"required"`
}

type line struct {
	Text string `json:"text"`
}

func room(_ context.Context, in *roomIn) (*zip.Socket[line], error) {
	return &zip.Socket[line]{Serve: func(conn *wsx.Conn) error {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return err
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(in.Room+":"+strings.ToUpper(string(msg)))); err != nil {
				return err
			}
		}
	}}, nil
}

func socketApp() *zip.App {
	app := zip.New(zip.Config{AppName: "chat", DisableStartupMessage: true})
	app.Get("/v1/rooms", room)
	return app
}

// serveHTTP starts app on a fresh port and answers its host:port once it
// accepts connections.
func serveHTTP(t *testing.T, app *zip.App) string {
	t.Helper()
	addr := freeAddr(t)
	go func() { _ = app.Listen("http://" + addr) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never accepted", addr)
	return ""
}

func TestSocket_UpgradesAfterTheContract(t *testing.T) {
	addr := serveHTTP(t, socketApp())
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}

	conn, resp, err := d.Dial("ws://"+addr+"/v1/rooms?room=r1", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("handshake = %d", resp.StatusCode)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := conn.ReadMessage(); err != nil || string(got) != "r1:HI" {
		t.Fatalf("got %q, %v", got, err)
	}

	// The input is validated before anything upgrades.
	_, resp, err = d.Dial("ws://"+addr+"/v1/rooms", nil)
	if err == nil || resp == nil || resp.StatusCode != 400 {
		t.Errorf("an invalid request upgraded: %v %v", resp, err)
	}
}

// A request that does not ask to upgrade is told how, as wsx tells it.
func TestSocket_APlainRequestIsTold426(t *testing.T) {
	resp, err := socketApp().Test(httptest.NewRequest("GET", "/v1/rooms?room=r1", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 426 || resp.Header.Get("Upgrade") != "websocket" || !strings.Contains(string(body), "WebSocket") {
		t.Errorf("%d %q %s", resp.StatusCode, resp.Header.Get("Upgrade"), body)
	}
}

func TestSocket_IsPublishedAsAnUpgrade(t *testing.T) {
	app := socketApp()
	op := opIn(t, app, "/v1/rooms", "get")
	if op["x-socket"] != "websocket" {
		t.Errorf("x-socket = %v", op["x-socket"])
	}
	up, ok := op["responses"].(map[string]any)["101"].(map[string]any)
	if !ok {
		t.Fatalf("responses = %v, want 101", op["responses"])
	}
	if ref, _ := up["x-events"].(map[string]any)["$ref"].(string); !strings.HasSuffix(ref, "/line") {
		t.Errorf("101 = %v, want the message type", up)
	}

	built(t, app)
	for _, tool := range app.MCPTools() {
		t.Errorf("a socket is a tool: %v", tool["name"])
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Result struct {
			Meta struct {
				Refused map[string]string `json:"refused"`
			} `json:"_meta"`
		} `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if got := env.Result.Meta.Refused[zip.ID("GET", "/v1/rooms")]; got != zip.NotACall {
		t.Errorf("tools/list _meta.refused = %v", env.Result.Meta.Refused)
	}
}

// A socket op may only be a GET, and its upgrade is the whole answer.
func TestSocket_RefusesWhatCannotUpgrade(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a POST that upgrades registered")
		}
	}()
	zip.New(zip.Config{DisableStartupMessage: true}).Post("/v1/rooms", room)
}
