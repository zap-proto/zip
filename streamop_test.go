// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A streamed answer is published as the stream it is. Without the manifest's
// stream field an op with no Out projected as a 204, which is a document
// promising an empty answer from an op that answers for minutes.

func streamed() zip.Manifest {
	return zip.Manifest{
		Manifest: zip.ManifestVersion,
		App:      "chat",
		Ops: []zip.ManifestOp{
			{Method: "POST", Path: "/v1/chat", In: "Prompt", Stream: "sse"},
			{Method: "POST", Path: "/v1/relay", Raw: true, Stream: "bytes"},
		},
		Types: []zip.TypeDesc{{
			ID: "Prompt", Name: "Prompt", Kind: "struct",
			Fields: []zip.FieldDesc{{Name: "text", JSON: "text", Type: zip.TypeRef{Prim: "string"}}},
		}},
	}
}

func TestProjectOpenAPIStreams(t *testing.T) {
	m := streamed()
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	m.Settle()
	b, err := json.Marshal(zip.ProjectOpenAPI(m))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]json.RawMessage `json:"content"`
			} `json:"requestBody"`
			Responses map[string]struct {
				Content map[string]json.RawMessage `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	chat := doc.Paths["/v1/chat"]["post"]
	if _, ok := chat.Responses["200"].Content["text/event-stream"]; !ok {
		t.Errorf("chat answers %s, want a text/event-stream 200", b)
	}
	if _, ok := chat.Responses["204"]; ok {
		t.Errorf("chat is published as answering nothing")
	}
	if _, ok := chat.RequestBody.Content["application/json"]; !ok {
		t.Errorf("chat's prompt is not a JSON body")
	}
	relay := doc.Paths["/v1/relay"]["post"]
	if got := string(relay.Responses["200"].Content["application/octet-stream"]); !strings.Contains(got, `"binary"`) {
		t.Errorf("relay answers %q, want binary", got)
	}
	if got := string(relay.RequestBody.Content["application/octet-stream"]); !strings.Contains(got, `"binary"`) {
		t.Errorf("relay reads %q, want binary", got)
	}
}

func TestCheckRefusesACrookedStream(t *testing.T) {
	for name, op := range map[string]zip.ManifestOp{
		"unknown stream": {Method: "GET", Path: "/a", Stream: "ws"},
		"stream and out": {Method: "GET", Path: "/a", Stream: "sse", Out: "Prompt"},
		"raw and in":     {Method: "POST", Path: "/a", Raw: true, In: "Prompt"},
	} {
		m := streamed()
		m.Ops = []zip.ManifestOp{op}
		if err := m.Check(); err == nil {
			t.Errorf("%s: checked clean", name)
		}
	}
}
