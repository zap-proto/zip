// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A form post — an OAuth token request, an upload with its fields — binds
// `form:` fields and *zip.File parts, and a `cookie:` field reads a request
// cookie. Each is a typed op with the wire it always had.

type tokenIn struct {
	Grant  string   `json:"grant_type" form:"grant_type"`
	Code   string   `json:"code" form:"code"`
	Scope  []string `json:"scope" form:"scope"`
	Device string   `json:"device" cookie:"device"`
}

type tokenOut struct {
	Grant  string   `json:"grant_type"`
	Code   string   `json:"code"`
	Scope  []string `json:"scope"`
	Device string   `json:"device"`
}

func token(_ context.Context, in *tokenIn) (*tokenOut, error) {
	return &tokenOut{Grant: in.Grant, Code: in.Code, Scope: in.Scope, Device: in.Device}, nil
}

func tokenApp(opts ...zip.OpOption) *zip.App {
	app := zip.New(zip.Config{AppName: "auth", DisableStartupMessage: true})
	zip.Describe("POST /v1/token", zip.Doc{
		Description: "Token trades a grant for a token.",
		Fields: map[string]string{
			"tokenIn.grant_type": "Grant names the grant.",
			"tokenIn.code":       "Code is the authorization code.",
			"tokenIn.scope":      "Scope lists what is asked for.",
			"tokenIn.device":     "Device is the browser's device cookie.",
		},
	})
	app.Post("/v1/token", token, opts...)
	return app
}

func postForm(t *testing.T, app *zip.App, media string, body []byte, cookie string) (int, tokenOut) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", media)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var out tokenOut
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestForm_BindsFieldsAndCookies(t *testing.T) {
	app := tokenApp()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"c1"}, "scope": {"read", "write"}}
	code, got := postForm(t, app, "application/x-www-form-urlencoded", []byte(form.Encode()), "device=d9; other=x")
	if code != 200 || got.Grant != "authorization_code" || got.Code != "c1" ||
		strings.Join(got.Scope, ",") != "read,write" || got.Device != "d9" {
		t.Errorf("%d %+v", code, got)
	}
}

// A form op reads JSON only when it says it takes JSON. Otherwise a JSON body
// binds nothing, exactly as a form reader finds no fields in it.
func TestForm_ReadsJSONOnlyWhenItSaysSo(t *testing.T) {
	body := []byte(`{"grant_type":"refresh_token","code":"c2"}`)
	if _, got := postForm(t, tokenApp(), "application/json", body, ""); got.Grant != "" || got.Code != "" {
		t.Errorf("a form op bound a JSON body it does not take: %+v", got)
	}
	both := tokenApp(zip.Consumes("application/x-www-form-urlencoded", "application/json"))
	if _, got := postForm(t, both, "application/json", body, ""); got.Grant != "refresh_token" || got.Code != "c2" {
		t.Errorf("a form op that takes JSON bound %+v", got)
	}
}

func TestForm_IsPublishedAsAForm(t *testing.T) {
	op := opIn(t, tokenApp(), "/v1/token", "post")
	content := op["requestBody"].(map[string]any)["content"].(map[string]any)
	form, ok := content["application/x-www-form-urlencoded"].(map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %v, want one url-encoded form", content)
	}
	props := form["schema"].(map[string]any)["properties"].(map[string]any)
	for name, want := range map[string]string{
		"grant_type": "Grant names the grant.", "code": "Code is the authorization code.", "scope": "Scope lists what is asked for.",
	} {
		if p, _ := props[name].(map[string]any); p["description"] != want {
			t.Errorf("form %s = %v", name, props[name])
		}
	}
	if _, ok := props["device"]; ok {
		t.Error("the cookie is published as a form field")
	}
	var cookie map[string]any
	for _, p := range op["parameters"].([]any) {
		if m := p.(map[string]any); m["in"] == "cookie" {
			cookie = m
		}
	}
	if cookie["name"] != "device" || cookie["description"] != "Device is the browser's device cookie." {
		t.Errorf("cookie parameter = %v", cookie)
	}
}

// A multipart form binds its parts: a *zip.File is one part, a []zip.File every
// part of that name, and the fields beside them bind as a url-encoded form's do.

type uploadIn struct {
	Title  string     `json:"title" form:"title"`
	Avatar *zip.File  `json:"avatar" form:"avatar"`
	Pages  []zip.File `json:"pages" form:"pages"`
}

type uploadOut struct {
	Title  string   `json:"title"`
	Avatar string   `json:"avatar"`
	Pages  []string `json:"pages"`
}

func upload(_ context.Context, in *uploadIn) (*uploadOut, error) {
	out := &uploadOut{Title: in.Title}
	if in.Avatar != nil {
		out.Avatar = in.Avatar.Name + "|" + in.Avatar.Type + "|" + string(in.Avatar.Bytes)
	}
	for _, p := range in.Pages {
		out.Pages = append(out.Pages, p.Name+"|"+string(p.Bytes))
	}
	return out, nil
}

func TestForm_BindsMultipartParts(t *testing.T) {
	app := zip.New(zip.Config{AppName: "up", DisableStartupMessage: true})
	app.Post("/v1/upload", upload)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", "Q3")
	part := func(name, file, media, body string) {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+name+`"; filename="`+file+`"`)
		h.Set("Content-Type", media)
		w, _ := mw.CreatePart(h)
		_, _ = w.Write([]byte(body))
	}
	part("avatar", "me.png", "image/png", "PNG")
	part("pages", "1.txt", "text/plain", "one")
	part("pages", "2.txt", "text/plain", "two")
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var got uploadOut
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != 200 || got.Title != "Q3" || got.Avatar != "me.png|image/png|PNG" ||
		strings.Join(got.Pages, ",") != "1.txt|one,2.txt|two" {
		t.Errorf("%d %+v", resp.StatusCode, got)
	}

	op := opIn(t, app, "/v1/upload", "post")
	content := op["requestBody"].(map[string]any)["content"].(map[string]any)
	form, ok := content["multipart/form-data"].(map[string]any)
	if !ok {
		t.Fatalf("content = %v, want multipart/form-data", content)
	}
	props := form["schema"].(map[string]any)["properties"].(map[string]any)
	if a := props["avatar"].(map[string]any); a["format"] != "binary" {
		t.Errorf("avatar = %v, want binary", a)
	}
	if p := props["pages"].(map[string]any); p["items"].(map[string]any)["format"] != "binary" {
		t.Errorf("pages = %v, want a list of binary", p)
	}

	// Over MCP a part is {name, type, bytes}, its bytes base64.
	built(t, app)
	env := mcpCall(t, app, zip.ID("POST", "/v1/upload"), map[string]any{
		"title":  "Q4",
		"avatar": map[string]any{"name": "a.png", "type": "image/png", "bytes": base64.StdEncoding.EncodeToString([]byte("PNG"))},
	})
	var viaMCP uploadOut
	if err := json.Unmarshal([]byte(mcpText(t, env["result"].(map[string]any))), &viaMCP); err != nil {
		t.Fatal(err)
	}
	if viaMCP.Title != "Q4" || viaMCP.Avatar != "a.png|image/png|PNG" {
		t.Errorf("over MCP: %+v", viaMCP)
	}
}

// A cookie field is not a URL value: a link could otherwise set a session.
func TestForm_CookieIsNotAQueryValue(t *testing.T) {
	type in struct {
		Session string `json:"session" cookie:"session"`
	}
	app := zip.New(zip.Config{AppName: "c", DisableStartupMessage: true})
	app.Get("/v1/me", func(_ context.Context, in *in) (*tokenOut, error) { return &tokenOut{Device: in.Session}, nil })
	req := httptest.NewRequest("GET", "/v1/me?session=forged", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var got tokenOut
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Device != "" {
		t.Errorf("a query value set the cookie field: %q", got.Device)
	}
	for _, p := range opIn(t, app, "/v1/me", "get")["parameters"].([]any) {
		if m := p.(map[string]any); m["in"] == "query" {
			t.Errorf("the cookie is published as a query parameter: %v", m)
		}
	}
}
