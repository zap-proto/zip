// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The answers a typed op gives besides one JSON value: a relay's bytes, a
// redirect, cookies, and JSON under its own media type. Each keeps its wire and
// says what it is in the document.

// upstreamShape is what an upstream answers on success.
type upstreamShape struct {
	Model string `json:"model"`
}

type passIn struct {
	Fail bool `json:"fail" url:"fail"`
}

func pass(_ context.Context, in *passIn) (*zip.Verbatim[upstreamShape], error) {
	if in.Fail {
		return &zip.Verbatim[upstreamShape]{Body: zip.Body{
			Type: "application/json", Bytes: []byte(`{"error":{"message":"overloaded"}}`), Status: 529,
			Header: map[string]string{"Retry-After": "3"},
		}}, nil
	}
	return &zip.Verbatim[upstreamShape]{Body: zip.Body{
		Type: "application/json; charset=utf-8", Bytes: []byte(`{"model":"m1" }`),
	}}, nil
}

// A relay writes the upstream's bytes, status and declared headers untouched,
// a refusal included, and the document describes the upstream's 2xx shape.
func TestVerbatim_RelaysAsSent(t *testing.T) {
	app := zip.New(zip.Config{AppName: "rl", DisableStartupMessage: true})
	app.Post("/v1/relay", pass, zip.WithResponseHeader("Retry-After"))

	for _, tc := range []struct {
		query, body, media, retry string
		code                      int
	}{
		{"", `{"model":"m1" }`, "application/json; charset=utf-8", "", 200},
		{"?fail=true", `{"error":{"message":"overloaded"}}`, "application/json", "3", 529},
	} {
		resp, err := app.Test(httptest.NewRequest("POST", "/v1/relay"+tc.query, nil))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != tc.code || string(b) != tc.body || resp.Header.Get("Content-Type") != tc.media ||
			resp.Header.Get("Retry-After") != tc.retry {
			t.Errorf("%q: %d %q %q %q", tc.query, resp.StatusCode, b, resp.Header.Get("Content-Type"), resp.Header.Get("Retry-After"))
		}
	}

	op := opIn(t, app, "/v1/relay", "post")
	resp := op["responses"].(map[string]any)
	ref, _ := resp["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
	if !strings.HasSuffix(ref, "/upstreamShape") {
		t.Errorf("200 = %v, want the upstream's shape", resp["200"])
	}
	refusal := resp["default"].(map[string]any)
	if _, ok := refusal["content"].(map[string]any)["*/*"]; !ok {
		t.Errorf("default = %v, want the upstream's refusal beside this service's", refusal)
	}
}

// A redirect answers its 3xx with Location and no body; the document names the
// status and the header.
func TestRedirect_SendsTheClientOn(t *testing.T) {
	app := zip.New(zip.Config{AppName: "oauth", DisableStartupMessage: true})
	app.Get("/v1/login", func(context.Context, *struct{}) (*zip.Redirect, error) {
		return &zip.Redirect{To: "https://id.example/authorize?state=s1"}, nil
	})
	app.Get("/v1/moved", func(context.Context, *struct{}) (*zip.Redirect, error) {
		return &zip.Redirect{To: "/v1/here"}, nil
	}, zip.WithStatus(308))

	for path, want := range map[string]struct {
		code int
		to   string
	}{"/v1/login": {302, "https://id.example/authorize?state=s1"}, "/v1/moved": {308, "/v1/here"}} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want.code || resp.Header.Get("Location") != want.to || len(b) != 0 {
			t.Errorf("%s: %d %q %q", path, resp.StatusCode, resp.Header.Get("Location"), b)
		}
	}
	resp := opIn(t, app, "/v1/login", "get")["responses"].(map[string]any)
	found, ok := resp["302"].(map[string]any)
	if !ok {
		t.Fatalf("responses = %v, want a 302", resp)
	}
	if _, ok := found["headers"].(map[string]any)["Location"]; !ok || found["content"] != nil {
		t.Errorf("302 = %v, want Location and no body", found)
	}
}

// An answer that sets cookies sets one Set-Cookie per cookie, which a header
// map cannot say.

type session struct {
	User string `json:"user"`
}

func (session) Cookies() []*http.Cookie {
	return []*http.Cookie{
		{Name: "sid", Value: "s1", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode},
		{Name: "csrf", Value: "c1", Path: "/"},
	}
}

func TestCookies_OneHeaderEach(t *testing.T) {
	app := zip.New(zip.Config{AppName: "s", DisableStartupMessage: true})
	app.Post("/v1/session", func(context.Context, *struct{}) (*session, error) { return &session{User: "z"}, nil })
	resp, err := app.Test(httptest.NewRequest("POST", "/v1/session", nil))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Header.Values("Set-Cookie")
	if len(got) != 2 || got[0] != "sid=s1; Path=/; HttpOnly; Secure; SameSite=Strict" || got[1] != "csrf=c1; Path=/" {
		t.Errorf("Set-Cookie = %q", got)
	}
	headers := opIn(t, app, "/v1/session", "post")["responses"].(map[string]any)["200"].(map[string]any)["headers"]
	if _, ok := headers.(map[string]any)["Set-Cookie"]; !ok {
		t.Errorf("headers = %v, want Set-Cookie", headers)
	}
}

// Produces names the media a JSON answer goes out as.
func TestProduces_NamesTheMedia(t *testing.T) {
	app := zip.New(zip.Config{AppName: "scim", DisableStartupMessage: true})
	app.Get("/v1/scim/Users/:id", func(_ context.Context, in *reportIn) (*mkOut, error) {
		return &mkOut{ID: in.ID}, nil
	}, zip.Produces("application/scim+json"))
	resp, err := app.Test(httptest.NewRequest("GET", "/v1/scim/Users/u1", nil))
	if err != nil {
		t.Fatal(err)
	}
	var got mkOut
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if resp.Header.Get("Content-Type") != "application/scim+json" || got.ID != "u1" {
		t.Errorf("%q %+v", resp.Header.Get("Content-Type"), got)
	}
	content := opIn(t, app, "/v1/scim/Users/{id}", "get")["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if _, ok := content["application/scim+json"]; !ok || len(content) != 1 {
		t.Errorf("content = %v", content)
	}
}

// A JSON answer without Produces is written exactly as before: the same bytes
// under the same Content-Type.
func TestAnswer_JSONWireIsUnchanged(t *testing.T) {
	app := zip.New(zip.Config{AppName: "j", DisableStartupMessage: true})
	app.Post("/v1/j", mk)
	resp, err := app.Test(httptest.NewRequest("POST", "/v1/j", strings.NewReader(`{"name":"x"}`)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Type") != "application/json; charset=utf-8" || string(b) != `{"id":"x"}` {
		t.Errorf("%q %s", resp.Header.Get("Content-Type"), b)
	}
}

// signIn is the callback leg of a browser sign-in: it sends the browser on and,
// on the same answer, clears the one-use flow cookie and sets the session.
type signIn struct {
	zip.Redirect
}

func (signIn) Cookies() []*http.Cookie {
	return []*http.Cookie{
		{Name: "flow", Value: "", Path: "/", MaxAge: -1},
		{Name: "sid", Value: "s1", Path: "/", HttpOnly: true},
	}
}

// confirmed is a page that also sets a cookie.
type confirmed struct {
	zip.Body
}

func (confirmed) Cookies() []*http.Cookie {
	return []*http.Cookie{{Name: "__Host-link", Value: "l1", Path: "/", Secure: true}}
}

// A redirect or a page that sets cookies is a type embedding Redirect or Body
// with a Cookies method: the redirect keeps its empty body, the page its
// bytes, and each Set-Cookie goes out on its own line.
func TestCookies_OnARedirectAndAPage(t *testing.T) {
	app := zip.New(zip.Config{AppName: "s", DisableStartupMessage: true})
	app.Get("/v1/callback", func(context.Context, *struct{}) (*signIn, error) {
		return &signIn{zip.Redirect{To: "/home"}}, nil
	})
	app.Get("/v1/linked", func(context.Context, *struct{}) (*confirmed, error) {
		return &confirmed{zip.Body{Type: "text/html; charset=utf-8", Bytes: []byte("<p>linked</p>")}}, nil
	}, zip.Produces("text/html"))

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/callback", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	got := resp.Header.Values("Set-Cookie")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/home" || len(b) != 0 || len(got) != 2 {
		t.Errorf("callback: %d %q %q %q", resp.StatusCode, resp.Header.Get("Location"), b, got)
	}
	if _, ok := resp.Header["Content-Type"]; ok {
		t.Errorf("callback carries Content-Type %q", resp.Header.Get("Content-Type"))
	}
	r302 := opIn(t, app, "/v1/callback", "get")["responses"].(map[string]any)["302"].(map[string]any)
	h := r302["headers"].(map[string]any)
	if _, ok := h["Set-Cookie"]; !ok || h["Location"] == nil {
		t.Errorf("302 headers = %v, want Location and Set-Cookie", h)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/v1/linked", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "<p>linked</p>" || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" ||
		resp.Header.Get("Set-Cookie") != "__Host-link=l1; Path=/; Secure" {
		t.Errorf("linked: %d %q %q %q", resp.StatusCode, b, resp.Header.Get("Content-Type"), resp.Header.Get("Set-Cookie"))
	}
	content := opIn(t, app, "/v1/linked", "get")["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if m, ok := content["text/html"].(map[string]any); !ok || m["schema"].(map[string]any)["format"] != "binary" {
		t.Errorf("linked content = %v, want binary text/html", content)
	}
}
