// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// Every kind of request and answer is a command. Bytes are named with @file or
// -, a multipart part with --field name=@file, a cookie is a flag
// that rides as one, and an answer that is not one JSON value is written as it
// is: bytes to the output or --out, events as they arrive, a redirect's
// Location, a WebSocket bridged to the terminal.

func kindsApp() *zip.App {
	app := zip.New(zip.Config{AppName: "kinds", DisableStartupMessage: true})
	app.Post("/v1/books/:org/scan", scan, zip.Consumes("application/pdf", "image/png"))
	app.Post("/v1/upload", upload)
	app.Post("/v1/token", token, zip.Consumes("application/x-www-form-urlencoded"))
	app.Get("/v1/reports/:id", report, zip.Produces("application/pdf"), zip.WithStatus(200, 202))
	app.Get("/v1/login", func(context.Context, *struct{}) (*zip.Redirect, error) {
		return &zip.Redirect{To: "https://id.example/authorize"}, nil
	})
	app.Post("/v1/ask", either)
	app.Get("/v1/rooms", room)
	app.Get("/v1/me", func(_ context.Context, in *meIn) (*mkOut, error) { return &mkOut{ID: in.Session}, nil })
	return app
}

// meIn reads a session cookie.
type meIn struct {
	Session string `json:"session" cookie:"session"`
}

// argv is the command line that reaches the op at method path.
func argv(t *testing.T, cmds []zip.Command, method, path string, rest ...string) []string {
	t.Helper()
	id := zip.ID(method, path)
	for _, c := range cmds {
		if c.OperationID == id {
			return append([]string{c.Service, c.Name}, rest...)
		}
	}
	t.Fatalf("no command for %s", id)
	return nil
}

// The two derivations — off the registry and off the document — are the same
// commands for every kind.
func TestCLIKinds_SpecAndRegistryAgree(t *testing.T) {
	app := kindsApp()
	spec, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	fromSpec, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	fromRegistry := app.Commands()
	if len(fromSpec) != len(fromRegistry) {
		t.Fatalf("spec has %d commands, registry %d", len(fromSpec), len(fromRegistry))
	}
	for i, r := range fromRegistry {
		s := fromSpec[i]
		if fmt.Sprint(r.Flags) != fmt.Sprint(s.Flags) || fmt.Sprint(r.Consumes) != fmt.Sprint(s.Consumes) || r.Stream != s.Stream {
			t.Errorf("%s:\n  registry %v %v %q\n  spec     %v %v %q", r.OperationID, r.Flags, r.Consumes, r.Stream, s.Flags, s.Consumes, s.Stream)
		}
	}
}

func TestCLIKinds_Local(t *testing.T) {
	app := kindsApp()
	dir := t.TempDir()
	pdf := filepath.Join(dir, "r.pdf")
	_ = os.WriteFile(pdf, []byte("%PDF\x00\xff"), 0o600)
	png := filepath.Join(dir, "me.png")
	_ = os.WriteFile(png, []byte("PNG"), 0o600)

	cmds := app.Commands()
	run := func(stdin string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cli := app.CLI()
		cli.Out = &out
		cli.In = strings.NewReader(stdin)
		if err := cli.Run(context.Background(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}

	var seen scanOut
	_ = json.Unmarshal([]byte(run("", argv(t, cmds, "POST", "/v1/books/:org/scan", "acme", "--kind", "fuel", "--body", "@"+pdf)...)), &seen)
	if seen.Org != "acme" || seen.Kind != "fuel" || seen.Size != 6 || seen.Type != "application/pdf" {
		t.Errorf("scan saw %+v", seen)
	}
	_ = json.Unmarshal([]byte(run("\x01\x02", argv(t, cmds, "POST", "/v1/books/:org/scan", "acme", "--body", "-")...)), &seen)
	if seen.Size != 2 {
		t.Errorf("scan from stdin saw %+v", seen)
	}

	var up uploadOut
	_ = json.Unmarshal([]byte(run("", argv(t, cmds, "POST", "/v1/upload", "--title", "Q3", "--field", "avatar=@"+png)...)), &up)
	if up.Title != "Q3" || up.Avatar != "me.png|image/png|PNG" {
		t.Errorf("upload saw %+v", up)
	}

	saved := filepath.Join(dir, "out.pdf")
	if got := run("", argv(t, cmds, "GET", "/v1/reports/:id", "q3", "--out", saved)...); got != "" {
		t.Errorf("--out still printed %q", got)
	}
	if b, _ := os.ReadFile(saved); string(b) != "%PDF-1.7 \x00\xff" {
		t.Errorf("--out wrote %q", b)
	}
	if got := run("", argv(t, cmds, "GET", "/v1/login")...); got != "https://id.example/authorize\n" {
		t.Errorf("redirect printed %q", got)
	}
	if got := run("", argv(t, cmds, "POST", "/v1/ask", "--stream")...); got != "{\"text\":\"a\"}\n{\"text\":\"b\"}\n[DONE]\n" {
		t.Errorf("stream printed %q", got)
	}
}

func TestCLIKinds_Remote(t *testing.T) {
	addr := serveHTTP(t, kindsApp())
	remote := zip.Remote{Base: "http://" + addr}
	spec, err := remote.Spec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cmds, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	png := filepath.Join(dir, "me.png")
	_ = os.WriteFile(png, []byte("PNG"), 0o600)
	run := func(stdin string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cli := &zip.CLI{Name: "kinds", Commands: cmds, Invoke: remote.Invoke, Out: &out, In: strings.NewReader(stdin)}
		if err := cli.Run(context.Background(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}

	var seen scanOut
	_ = json.Unmarshal([]byte(run("\x00\x01\x02", argv(t, cmds, "POST", "/v1/books/:org/scan", "acme", "--kind", "fuel", "--body", "-")...)), &seen)
	if seen.Org != "acme" || seen.Kind != "fuel" || seen.Size != 3 || seen.Type != "application/pdf" {
		t.Errorf("scan saw %+v", seen)
	}
	var up uploadOut
	_ = json.Unmarshal([]byte(run("", argv(t, cmds, "POST", "/v1/upload", "--title", "Q3", "--field", "avatar=@"+png)...)), &up)
	if up.Title != "Q3" || up.Avatar != "me.png|image/png|PNG" {
		t.Errorf("upload saw %+v", up)
	}
	var tok tokenOut
	_ = json.Unmarshal([]byte(run("", argv(t, cmds, "POST", "/v1/token", "--grant-type", "code", "--code", "c1",
		"--scope", `["read","write"]`, "--device", "d9")...)), &tok)
	if tok.Grant != "code" || tok.Code != "c1" || strings.Join(tok.Scope, ",") != "read,write" || tok.Device != "d9" {
		t.Errorf("token saw %+v", tok)
	}
	if got := run("", argv(t, cmds, "GET", "/v1/reports/:id", "q3")...); got != "%PDF-1.7 \x00\xff" {
		t.Errorf("report printed %q", got)
	}
	if got := run("", argv(t, cmds, "GET", "/v1/login")...); got != "https://id.example/authorize\n" {
		t.Errorf("redirect printed %q", got)
	}
	if got := run("", argv(t, cmds, "POST", "/v1/ask", "--stream")...); got != "data: {\"text\":\"a\"}\n\ndata: {\"text\":\"b\"}\n\ndata: [DONE]\n\n" {
		t.Errorf("stream printed %q", got)
	}
	if got := run("hello\n", argv(t, cmds, "GET", "/v1/rooms", "--room", "r1")...); got != "r1:HELLO\n" {
		t.Errorf("socket printed %q", got)
	}
	var who mkOut
	_ = json.Unmarshal([]byte(run("", argv(t, cmds, "GET", "/v1/me", "--session", "s1")...)), &who)
	if who.ID != "s1" {
		t.Errorf("cookie op saw %+v", who)
	}
}
