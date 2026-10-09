// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// An input that decodes its own body — YAML or JSON, an RFC 7386 merge patch, a
// strict decode that refuses an unknown field, a cap on its own size — is a
// zip.Parser, and stays a typed op: the document publishes its reflected schema
// under the media it consumes.

type patchIn struct {
	ID   string   `json:"id" url:"id"`
	Name *string  `json:"name"`
	Tags []string `json:"tags"`
	// sent names the members the patch carried, null ones included: RFC 7386
	// tells "clear it" from "leave it" by presence.
	sent []string
}

const patchCap = 64

func (p *patchIn) Parse(media string, body []byte) error {
	if len(body) > patchCap {
		return zip.Errorf(413, "a patch is at most %d bytes", patchCap)
	}
	if strings.HasPrefix(media, "application/yaml") {
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(k) != "name" {
				return fmt.Errorf("line %q is not name: <value>", line)
			}
			name := strings.TrimSpace(v)
			p.Name, p.sent = &name, append(p.sent, "name")
		}
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return err
	}
	for k := range members {
		p.sent = append(p.sent, k)
	}
	sort.Strings(p.sent)
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(p)
}

type patchOut struct {
	ID   string   `json:"id"`
	Name *string  `json:"name"`
	Sent []string `json:"sent"`
}

func patchApp() *zip.App {
	app := zip.New(zip.Config{AppName: "rec", DisableStartupMessage: true})
	app.Patch("/v1/records/:id", func(_ context.Context, in *patchIn) (*patchOut, error) {
		return &patchOut{ID: in.ID, Name: in.Name, Sent: in.sent}, nil
	}, zip.Consumes("application/merge-patch+json", "application/yaml"))
	return app
}

func sendPatch(t *testing.T, app *zip.App, media, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/v1/records/r1", strings.NewReader(body))
	req.Header.Set("Content-Type", media)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

func TestParser_DecodesItsOwnBody(t *testing.T) {
	app := patchApp()
	for _, tc := range []struct {
		media, body string
		code        int
		has         string
	}{
		{"application/merge-patch+json", `{"name":null}`, 200, `"id":"r1","name":null,"sent":["name"]`},
		{"application/merge-patch+json", `{"name":"b","tags":["x"]}`, 200, `"name":"b","sent":["name","tags"]`},
		{"application/yaml", "name: c\n", 200, `"name":"c","sent":["name"]`},
		{"application/merge-patch+json", `{"nmae":"typo"}`, 400, `unknown field`},
		{"application/merge-patch+json", `{"name":"` + strings.Repeat("x", 80) + `"}`, 413, `at most 64 bytes`},
	} {
		code, body := sendPatch(t, app, tc.media, tc.body)
		if code != tc.code || !strings.Contains(body, tc.has) {
			t.Errorf("%s %s: %d %s, want %d with %s", tc.media, tc.body, code, body, tc.code, tc.has)
		}
	}
}

// The document publishes the input's own schema under each media it consumes.
func TestParser_PublishesItsSchema(t *testing.T) {
	op := opIn(t, patchApp(), "/v1/records/{id}", "patch")
	content := op["requestBody"].(map[string]any)["content"].(map[string]any)
	for _, media := range []string{"application/merge-patch+json", "application/yaml"} {
		ref, _ := content[media].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
		if !strings.HasSuffix(ref, "/patchIn") {
			t.Errorf("%s = %v, want the input's schema", media, content[media])
		}
	}
}

// Over MCP the argument object is handed to Parse as application/json.
func TestParser_TakesTheArgumentObject(t *testing.T) {
	app := patchApp()
	built(t, app)
	env := mcpCall(t, app, zip.ID("PATCH", "/v1/records/:id"), map[string]any{"id": "r2", "name": "d"})
	got := mcpText(t, env["result"].(map[string]any))
	if !strings.Contains(got, `"name":"d"`) {
		t.Errorf("over MCP: %s", got)
	}
}
