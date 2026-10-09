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
	// No keep-alive: a connection left idle would outlive the test.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	if resp, err := client.Get("http://" + addr + "/v1/bad"); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	resp, err := client.Get("http://" + addr + "/v1/good")
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

// A WebSocket op refuses a request that cannot become its connection before
// its handler runs — no upgrade, another version, no key, a browser on an
// origin it does not admit, the in-process CLI — and admits the origins it
// names.
func TestKinds_ASocketRefusesBeforeItsHandler(t *testing.T) {
	var ran atomic.Int32
	live := func(context.Context, *struct{}) (*zip.Socket[line], error) {
		ran.Add(1)
		return &zip.Socket[line]{Serve: func(*websocket.Conn) error { return nil }}, nil
	}
	app := zip.New(zip.Config{AppName: "ws", DisableStartupMessage: true})
	app.Get("/v1/live", live)
	app.Get("/v1/open", live, zip.Origins("https://app.example"))
	resp, err := app.Test(httptest.NewRequest("GET", "/v1/live", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 426 || ran.Load() != 0 {
		t.Errorf("a plain GET answered %d and ran the handler %d times", resp.StatusCode, ran.Load())
	}
	for _, c := range []struct {
		name   string
		header map[string]string
		code   int
	}{
		{"version 8", map[string]string{"Sec-WebSocket-Version": "8"}, 426},
		{"versions 8 and 7", map[string]string{"Sec-WebSocket-Version": "8, 7"}, 426},
		{"no key", map[string]string{"Sec-WebSocket-Key": ""}, 400},
		{"another site", map[string]string{"Origin": "https://elsewhere.example"}, 403},
		{"opaque origin", map[string]string{"Origin": "null"}, 403},
	} {
		req := httptest.NewRequest("GET", "/v1/live", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != c.code || ran.Load() != 0 {
			t.Errorf("%s answered %d, want %d; the handler ran %d times", c.name, resp.StatusCode, c.code, ran.Load())
		}
	}
	if err := app.CLI().Run(context.Background(), argv(t, app.Commands(), "GET", "/v1/live")); err == nil || ran.Load() != 0 {
		t.Errorf("the in-process CLI answered %v and ran the handler %d times", err, ran.Load())
	}

	addr := serveHTTP(t, app)
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	for _, c := range []struct {
		path, origin string
		ok           bool
	}{
		{"/v1/live", "http://" + addr, true},
		{"/v1/live", "https://app.example", false},
		{"/v1/open", "https://app.example", true},
		{"/v1/open", "https://elsewhere.example", false},
	} {
		conn, resp, err := d.Dial("ws://"+addr+c.path, http.Header{"Origin": {c.origin}})
		if err == nil {
			_ = conn.Close()
		}
		if c.ok != (err == nil) || !c.ok && (resp == nil || resp.StatusCode != 403) {
			t.Errorf("%s from %s: %v, %v", c.path, c.origin, resp, err)
		}
	}
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

type metaOut struct {
	// W is the width.
	W int `json:"w"`
}

// An op that answers JSON or image bytes names the bytes' media with Produces,
// and its JSON still goes out as JSON.
func TestKinds_JSONBesideBytesStaysJSON(t *testing.T) {
	app := zip.New(zip.Config{AppName: "img", DisableStartupMessage: true})
	app.Get("/v1/thumb", func(context.Context, *struct{}) (*zip.Or[metaOut, zip.Body], error) {
		return &zip.Or[metaOut, zip.Body]{A: &metaOut{W: 64}}, nil
	}, zip.Produces("image/png"))
	resp, err := app.Test(httptest.NewRequest("GET", "/v1/thumb", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" || string(b) != `{"w":64}` {
		t.Errorf("answered %q %s", ct, b)
	}
}

// A 3xx that is not a redirect carries the body it sends, 204 and 304 carry
// none, and a redirect's 3xx carries a Location and no body. The doc comment's
// example rides every status that answers the Out.
func TestKinds_WhichStatusesCarryABody(t *testing.T) {
	app := zip.New(zip.Config{AppName: "st", DisableStartupMessage: true})
	zip.Describe("POST /v1/see", zip.Doc{Description: "See answers.", Response: json.RawMessage(`{"id":"x"}`)})
	app.Post("/v1/see", func(context.Context, *struct{}) (*multiOut, error) {
		return &multiOut{ID: "x", code: 302}, nil
	}, zip.WithStatus(200, 201, 302, 304))
	app.Get("/v1/go", func(context.Context, *struct{}) (*zip.Redirect, error) {
		return &zip.Redirect{To: "/"}, nil
	}, zip.WithStatus(307))
	see := opIn(t, app, "/v1/see", "post")["responses"].(map[string]any)
	for code, want := range map[string]bool{"200": true, "201": true, "302": true, "304": false} {
		entry := see[code].(map[string]any)
		content, has := entry["content"].(map[string]any)
		if has != want {
			t.Errorf("%s carries content %v, want %v", code, has, want)
			continue
		}
		if want && content["application/json"].(map[string]any)["example"] == nil {
			t.Errorf("%s carries no example", code)
		}
	}
	if r := opIn(t, app, "/v1/go", "get")["responses"].(map[string]any)["307"].(map[string]any); r["content"] != nil {
		t.Errorf("a redirect's 307 carries content: %v", r)
	}
}
