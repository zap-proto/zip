// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/websocket"

	"github.com/zap-proto/zip"
)

// The new kinds leave every op that predates them where it was, and each kind
// holds the edges an adversary reaches for. Each test here is one of those
// edges, driven.

type multiOut struct {
	// ID names the record.
	ID   string `json:"id"`
	code int
}

func (m multiOut) StatusCode() int { return m.code }

// An op that declares several statuses answers its Out under each, so each
// carries the Out's schema — a held op's 202 included.
func TestKinds_EveryDeclaredStatusCarriesTheAnswer(t *testing.T) {
	app := zip.New(zip.Config{AppName: "multi", DisableStartupMessage: true})
	app.Post("/v1/records", func(context.Context, *struct{}) (*multiOut, error) {
		return &multiOut{ID: "r1", code: 201}, nil
	}, zip.WithStatus(200, 201, 202))
	responses := opIn(t, app, "/v1/records", "post")["responses"].(map[string]any)
	for _, code := range []string{"200", "201", "202"} {
		entry := responses[code].(map[string]any)
		content, _ := entry["content"].(map[string]any)
		media, _ := content["application/json"].(map[string]any)
		if b, _ := json.Marshal(media["schema"]); !strings.Contains(string(b), "multiOut") {
			t.Errorf("%s = %v, want the Out's schema", code, entry)
		}
	}
}

type quietOut struct {
	OK bool `json:"ok"`
}

func (quietOut) StatusCode() int { return 0 }

// A value of the op's own type that states zero states a status it did not
// declare, and is refused as it always was; only zip's Body and Redirect read
// zero as the op's own.
func TestKinds_ZeroIsUndeclaredForAnOrdinaryValue(t *testing.T) {
	app := zip.New(zip.Config{AppName: "zero", DisableStartupMessage: true})
	app.Get("/v1/quiet", func(context.Context, *struct{}) (*quietOut, error) { return &quietOut{OK: true}, nil })
	resp, err := app.Test(httptest.NewRequest("GET", "/v1/quiet", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 500 {
		t.Errorf("a StatusCode of 0 answered %d", resp.StatusCode)
	}
}

type tenantIn struct {
	// Tenant is the tenant the request is for.
	Tenant string `json:"tenant" header:"X-Tenant"`
}

// A field a header binds is a flag under its field's name, as it always was,
// and it reaches the op.
func TestKinds_AHeaderFieldKeepsItsFlag(t *testing.T) {
	app := zip.New(zip.Config{AppName: "things", DisableStartupMessage: true})
	app.Get("/v1/things/:id", func(_ context.Context, in *struct {
		ID     string `json:"id" url:"id"`
		Tenant string `json:"tenant" header:"X-Tenant"`
	}) (*mkOut, error) {
		return &mkOut{ID: in.Tenant + "/" + in.ID}, nil
	})
	var out bytes.Buffer
	cli := app.CLI()
	cli.Out = &out
	cmds := app.Commands()
	if err := cli.Run(context.Background(), argv(t, cmds, "GET", "/v1/things/:id", "x1", "--tenant", "acme")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"acme/x1"`) {
		t.Errorf("printed %q", out.String())
	}
}

// A reader that panics ends its answer, not the process: the next request is
// answered.
func TestKinds_APanickingReaderEndsOnlyItsAnswer(t *testing.T) {
	app := zip.New(zip.Config{AppName: "dl", DisableStartupMessage: true})
	app.Get("/v1/bad", func(context.Context, *struct{}) (*zip.Body, error) {
		return &zip.Body{Type: "text/plain", Reader: panicky{}}, nil
	})
	app.Get("/v1/good", func(context.Context, *struct{}) (*mkOut, error) { return &mkOut{ID: "ok"}, nil })
	addr := serveHTTP(t, app)
	if resp, err := http.Get("http://" + addr + "/v1/bad"); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	resp, err := http.Get("http://" + addr + "/v1/good")
	if err != nil {
		t.Fatalf("the process did not survive: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("good answered %d", resp.StatusCode)
	}
}

type panicky struct{}

func (panicky) Read([]byte) (int, error) { panic("reader broke") }

// Over MCP, bytes past the tool bound are cut and say so, and bytes that state
// no type are the op's own media, not text.
func TestKinds_ToolBytesAreBoundedAndTyped(t *testing.T) {
	app := zip.New(zip.Config{AppName: "blob", DisableStartupMessage: true})
	app.Get("/v1/big", func(context.Context, *struct{}) (*zip.Body, error) {
		return &zip.Body{Type: "text/plain", Bytes: bytes.Repeat([]byte("a"), 3<<20)}, nil
	})
	app.Get("/v1/raw", func(context.Context, *struct{}) (*zip.Body, error) {
		return &zip.Body{Bytes: []byte{0xff, 0xfe, 0x00, 0x80, 'P', 'N', 'G'}}, nil
	})
	built(t, app)
	big := mcpCall(t, app, zip.ID("GET", "/v1/big"), nil)["result"].(map[string]any)
	if n := len(mcpText(t, big)); n != 1<<20 {
		t.Errorf("tool text is %d bytes, want the 1 MiB bound", n)
	}
	if meta, _ := big["_meta"].(map[string]any); meta["truncated"] != true {
		t.Errorf("_meta = %v, want truncated", big["_meta"])
	}
	raw := mcpCall(t, app, zip.ID("GET", "/v1/raw"), nil)["result"].(map[string]any)
	first := raw["content"].([]any)[0].(map[string]any)
	if first["type"] != "resource" || first["resource"].(map[string]any)["mimeType"] != "application/octet-stream" {
		t.Errorf("untyped bytes came back as %v", first)
	}
}

// A WebSocket op refuses a request that cannot upgrade before its handler
// runs, and a browser on another site cannot open it.
func TestKinds_ASocketRefusesBeforeItsHandler(t *testing.T) {
	var ran atomic.Int32
	app := zip.New(zip.Config{AppName: "ws", DisableStartupMessage: true})
	app.Get("/v1/live", func(context.Context, *struct{}) (*zip.Socket[line], error) {
		ran.Add(1)
		return &zip.Socket[line]{Serve: func(*websocket.Conn) error { return nil }}, nil
	})
	resp, err := app.Test(httptest.NewRequest("GET", "/v1/live", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 426 || ran.Load() != 0 {
		t.Errorf("a plain GET answered %d and ran the handler %d times", resp.StatusCode, ran.Load())
	}
	addr := serveHTTP(t, app)
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	h := http.Header{"Origin": {"https://elsewhere.example"}}
	if conn, resp, err := d.Dial("ws://"+addr+"/v1/live", h); err == nil {
		_ = conn.Close()
		t.Error("a cross-site origin opened the socket")
	} else if resp == nil || resp.StatusCode != 403 {
		t.Errorf("a cross-site origin got %v, %v", resp, err)
	}
	conn, _, err := d.Dial("ws://"+addr+"/v1/live", http.Header{"Origin": {"http://" + addr}})
	if err != nil {
		t.Fatalf("the socket's own origin was refused: %v", err)
	}
	_ = conn.Close()
}

// A 3xx that carries a body is read as an answer by the remote CLI; one that
// carries none is a redirect.
func TestKinds_ARedirectWithABodyIsAnAnswer(t *testing.T) {
	app := zip.New(zip.Config{AppName: "moved", DisableStartupMessage: true})
	app.Get("/v1/moved", func(context.Context, *struct{}) (*multiOut, error) {
		return &multiOut{ID: "there", code: 303}, nil
	}, zip.WithStatus(303), zip.WithResponseHeader("Location"))
	addr := serveHTTP(t, app)
	remote := zip.Remote{Base: "http://" + addr}
	spec, err := remote.Spec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cmds, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cli := &zip.CLI{Name: "moved", Commands: cmds, Invoke: remote.Invoke, Out: &out}
	if err := cli.Run(context.Background(), argv(t, cmds, "GET", "/v1/moved")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"there"`) {
		t.Errorf("printed %q, want the body", out.String())
	}
}

// The JSON alternative of an answer that may also stream crosses the call
// plane as the union it is.
func TestKinds_AValueOrStreamCrossesTheCallPlane(t *testing.T) {
	app := zip.New(zip.Config{AppName: "either", DisableStartupMessage: true})
	app.Post("/v1/either", func(context.Context, *struct{}) (*zip.Or[mkOut, zip.Sse[mkOut]], error) {
		return &zip.Or[mkOut, zip.Sse[mkOut]]{A: &mkOut{ID: "whole"}}, nil
	}, zip.WithOperationID("either"))
	c, err := zip.Dial(serveUDS(t, app))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	got, err := zip.Call[struct{}, zip.Or[mkOut, zip.Sse[mkOut]]](context.Background(), c, "either", &struct{}{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.A == nil || got.A.ID != "whole" || got.B != nil {
		t.Errorf("got %+v", got)
	}
}
