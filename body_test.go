// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// An op whose request body is bytes — an upload, a statement, a signed webhook
// — takes it as a zip.Body field and is a typed op like any other: the bytes
// and their media type arrive untouched, and the input's other fields still
// bind from the URL.

type scanIn struct {
	Org  string   `json:"org" url:"org"`
	Kind string   `json:"kind"`
	Body zip.Body `json:"body"`
}

type scanOut struct {
	Org  string `json:"org"`
	Kind string `json:"kind"`
	Type string `json:"type"`
	Size int    `json:"size"`
	Sum  string `json:"sum"`
}

func scan(_ context.Context, in *scanIn) (*scanOut, error) {
	return &scanOut{Org: in.Org, Kind: in.Kind, Type: in.Body.Type, Size: len(in.Body.Bytes),
		Sum: base64.StdEncoding.EncodeToString(in.Body.Bytes)}, nil
}

func scanApp() *zip.App {
	app := zip.New(zip.Config{AppName: "books", DisableStartupMessage: true})
	zip.Describe("POST /v1/books/:org/scan", zip.Doc{
		Description: "Scan reads a receipt.",
		Fields: map[string]string{
			"scanIn.org":  "Org is the org whose books take the receipt.",
			"scanIn.kind": "Kind names what the receipt is.",
			"scanIn.body": "Body is the receipt, as the camera wrote it.",
		},
	})
	app.Post("/v1/books/:org/scan", scan, zip.Consumes("application/pdf", "image/png"))
	return app
}

// Bytes that are not JSON — here, not even UTF-8 — reach the handler exactly
// as sent, under the Content-Type they were sent with.
func TestBody_TakesTheBytesAsSent(t *testing.T) {
	app := scanApp()
	raw := []byte{0x25, 0x50, 0x44, 0x46, 0x00, 0xff, '{', 0x80}
	req := httptest.NewRequest("POST", "/v1/books/acme/scan?kind=fuel", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/pdf")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var got scanOut
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("status %d: %v", resp.StatusCode, err)
	}
	if resp.StatusCode != 200 || got.Org != "acme" || got.Kind != "fuel" || got.Type != "application/pdf" ||
		got.Sum != base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("status %d, handler saw %+v", resp.StatusCode, got)
	}
}

// The document says what the wire takes: the bytes under each media type the
// op consumes, the field's prose as the body's, and every other field as a
// parameter of the URL that carries it.
func TestBody_IsPublishedAsBinary(t *testing.T) {
	op := opIn(t, scanApp(), "/v1/books/{org}/scan", "post")
	body := op["requestBody"].(map[string]any)
	content := body["content"].(map[string]any)
	if len(content) != 2 {
		t.Fatalf("content = %v, want the two media the op consumes", content)
	}
	for _, media := range []string{"application/pdf", "image/png"} {
		schema := content[media].(map[string]any)["schema"].(map[string]any)
		if schema["type"] != "string" || schema["format"] != "binary" {
			t.Errorf("%s schema = %v, want binary", media, schema)
		}
	}
	if body["description"] != "Body is the receipt, as the camera wrote it." {
		t.Errorf("requestBody description = %v", body["description"])
	}
	params := map[string]map[string]any{}
	for _, p := range op["parameters"].([]any) {
		m := p.(map[string]any)
		params[m["in"].(string)+":"+m["name"].(string)] = m
	}
	if params["path:org"]["description"] != "Org is the org whose books take the receipt." {
		t.Errorf("path org = %v", params["path:org"])
	}
	if params["query:kind"]["description"] != "Kind names what the receipt is." {
		t.Errorf("query kind = %v, want the field the URL carries beside the bytes", params["query:kind"])
	}
	if _, ok := params["query:body"]; ok {
		t.Error("the body is offered as a query parameter")
	}
}

// Without Consumes the bytes are application/octet-stream.
func TestBody_DefaultsToOctets(t *testing.T) {
	app := zip.New(zip.Config{AppName: "up", DisableStartupMessage: true})
	app.Post("/v1/up", scan)
	content := opIn(t, app, "/v1/up", "post")["requestBody"].(map[string]any)["content"].(map[string]any)
	if _, ok := content["application/octet-stream"]; !ok || len(content) != 1 {
		t.Errorf("content = %v, want application/octet-stream alone", content)
	}
}

// Over MCP the input is one argument object, and the bytes are its base64
// string; their type is the first the op consumes.
func TestBody_CrossesMCPAsBase64(t *testing.T) {
	app := scanApp()
	var schema map[string]any
	for _, tool := range app.MCPTools() {
		if tool["name"] == zip.ID("POST", "/v1/books/:org/scan") {
			schema = tool["inputSchema"].(map[string]any)
		}
	}
	prop, _ := schema["properties"].(map[string]any)["body"].(map[string]any)
	if prop["type"] != "string" || prop["contentEncoding"] != "base64" {
		t.Fatalf("tool body = %v, want a base64 string", prop)
	}
	raw := []byte{0, 1, 2, 0xff}
	built(t, app)
	env := mcpCall(t, app, zip.ID("POST", "/v1/books/:org/scan"), map[string]any{
		"org": "acme", "kind": "fuel", "body": base64.StdEncoding.EncodeToString(raw),
	})
	var got scanOut
	if err := json.Unmarshal([]byte(mcpText(t, env["result"].(map[string]any))), &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "application/pdf" || got.Size != 4 || got.Org != "acme" {
		t.Errorf("handler saw %+v", got)
	}
}

// A shape the wire cannot carry is refused when it is registered.
func TestBody_RefusesWhatTheWireCannotCarry(t *testing.T) {
	type two struct {
		A zip.Body `json:"a"`
		B zip.Body `json:"b"`
	}
	type ptr struct {
		B *zip.Body `json:"b"`
	}
	for name, register := range map[string]func(*zip.App){
		"two bodies": func(a *zip.App) {
			a.Post("/x", func(context.Context, *two) (*struct{}, error) { return nil, nil })
		},
		"a body on GET": func(a *zip.App) {
			a.Get("/x", func(context.Context, *scanIn) (*struct{}, error) { return nil, nil })
		},
		"a pointer body": func(a *zip.App) {
			a.Post("/x", func(context.Context, *ptr) (*struct{}, error) { return nil, nil })
		},
		"consumes on GET": func(a *zip.App) {
			a.Get("/x", func(context.Context, *mkIn) (*struct{}, error) { return nil, nil }, zip.Consumes("text/plain"))
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: registered", name)
				}
			}()
			register(zip.New(zip.Config{DisableStartupMessage: true}))
		}()
	}
}

// A Body is also an answer: bytes under their media type, a filename to save
// them under, and a status of their own.

type reportIn struct {
	ID string `json:"id" url:"id"`
}

func report(_ context.Context, in *reportIn) (*zip.Body, error) {
	if in.ID == "late" {
		return &zip.Body{Type: "application/pdf", Bytes: []byte("%PDF-later"), Status: 202}, nil
	}
	return &zip.Body{Type: "application/pdf", Bytes: []byte("%PDF-1.7 \x00\xff"), Name: `report "q3".pdf`}, nil
}

func TestBody_AnswersBytes(t *testing.T) {
	app := zip.New(zip.Config{AppName: "rep", DisableStartupMessage: true})
	app.Get("/v1/reports/:id", report, zip.Produces("application/pdf"), zip.WithStatus(200, 202))

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/reports/q3", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "%PDF-1.7 \x00\xff" {
		t.Errorf("answered %d %q", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="report \"q3\".pdf"` {
		t.Errorf("Content-Disposition = %q", cd)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/v1/reports/late", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 202 || resp.Header.Get("Content-Disposition") != "" {
		t.Errorf("late answered %d with %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}

	op := opIn(t, app, "/v1/reports/{id}", "get")
	for _, code := range []string{"200", "202"} {
		entry := op["responses"].(map[string]any)[code].(map[string]any)
		content, _ := entry["content"].(map[string]any)
		media, ok := content["application/pdf"].(map[string]any)
		if !ok || media["schema"].(map[string]any)["format"] != "binary" {
			t.Errorf("%s = %v, want binary application/pdf", code, entry)
		}
		if _, ok := entry["headers"].(map[string]any)["Content-Disposition"]; !ok {
			t.Errorf("%s does not publish Content-Disposition", code)
		}
	}
}

// A Body with a Reader is written as the reader yields it, chunk by chunk.
func TestBody_StreamsAReader(t *testing.T) {
	app := zip.New(zip.Config{AppName: "csv", DisableStartupMessage: true})
	app.Get("/v1/export", func(context.Context, *struct{}) (*zip.Body, error) {
		return &zip.Body{Type: "text/csv", Reader: io.NopCloser(strings.NewReader("a,b\n1,2\n"))}, nil
	}, zip.Produces("text/csv"))
	resp, err := app.Test(httptest.NewRequest("GET", "/v1/export", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "a,b\n1,2\n" || resp.Header.Get("Content-Type") != "text/csv" {
		t.Errorf("answered %q as %q", b, resp.Header.Get("Content-Type"))
	}
	if te := resp.TransferEncoding; len(te) == 0 || te[0] != "chunked" {
		t.Errorf("transfer encoding = %v, want chunked", te)
	}
}

// A tools/call of an op that answers bytes reads them into tool content: text
// as text, an image as an image, anything else as an embedded resource blob.
func TestBody_AnswersMCPAsContent(t *testing.T) {
	app := zip.New(zip.Config{AppName: "rep", DisableStartupMessage: true})
	app.Get("/v1/reports/:id", report, zip.Produces("application/pdf"), zip.WithStatus(200, 202))
	app.Get("/v1/logo", func(context.Context, *struct{}) (*zip.Body, error) {
		return &zip.Body{Type: "image/png", Bytes: []byte{0x89, 'P', 'N', 'G'}}, nil
	}, zip.Produces("image/png"))
	app.Get("/v1/readme", func(context.Context, *struct{}) (*zip.Body, error) {
		return &zip.Body{Type: "text/markdown", Bytes: []byte("# hi")}, nil
	}, zip.Produces("text/markdown"))

	built(t, app)
	first := func(env map[string]any) map[string]any {
		return env["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	}
	pdf := first(mcpCall(t, app, zip.ID("GET", "/v1/reports/:id"), map[string]any{"id": "q3"}))
	res, _ := pdf["resource"].(map[string]any)
	if pdf["type"] != "resource" || res["mimeType"] != "application/pdf" ||
		res["blob"] != base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 \x00\xff")) {
		t.Errorf("pdf = %v", pdf)
	}
	if img := first(mcpCall(t, app, zip.ID("GET", "/v1/logo"), nil)); img["type"] != "image" || img["mimeType"] != "image/png" {
		t.Errorf("png = %v", img)
	}
	if txt := first(mcpCall(t, app, zip.ID("GET", "/v1/readme"), nil)); txt["type"] != "text" || txt["text"] != "# hi" {
		t.Errorf("markdown = %v", txt)
	}
}

// built builds app, which installs /mcp and the call plane.
func built(t *testing.T, app *zip.App) {
	t.Helper()
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

// opIn is one operation of an app's document.
func opIn(t *testing.T, app *zip.App, path, method string) map[string]any {
	t.Helper()
	b, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	op, ok := doc.Paths[path][method]
	if !ok {
		t.Fatalf("no %s %s in %s", method, path, b)
	}
	return op
}
