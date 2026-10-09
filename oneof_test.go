// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// A union is one of several shapes. zip.Or is the stock one; a type with a
// OneOf method names its own alternatives by the method's result types. Either
// way the document publishes oneOf, and an alternative that states its own
// status is filed under it.

type created struct {
	ID string `json:"id"`
}

func (created) StatusCode() int { return 201 }

type existing struct {
	ID      string `json:"id"`
	Existed bool   `json:"existed"`
}

func upsert(_ context.Context, in *mkIn) (*zip.Or[created, existing], error) {
	if in.Name == "old" {
		return &zip.Or[created, existing]{B: &existing{ID: "old", Existed: true}}, nil
	}
	return &zip.Or[created, existing]{A: &created{ID: in.Name}}, nil
}

func TestOr_FilesEachAlternativeUnderItsStatus(t *testing.T) {
	app := zip.New(zip.Config{AppName: "u", DisableStartupMessage: true})
	app.Post("/v1/things", upsert)

	for name, want := range map[string]struct {
		code int
		body string
	}{"new": {201, `{"id":"new"}`}, "old": {200, `{"id":"old","existed":true}`}} {
		code, body := call2(t, app, "POST", "/v1/things", `{"name":"`+name+`"}`)
		if code != want.code || body != want.body {
			t.Errorf("%s: %d %s, want %d %s", name, code, body, want.code, want.body)
		}
	}
	resp := opIn(t, app, "/v1/things", "post")["responses"].(map[string]any)
	for code, typ := range map[string]string{"201": "/created", "200": "/existing"} {
		ref, _ := resp[code].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
		if !strings.HasSuffix(ref, typ) {
			t.Errorf("%s = %v, want %s", code, resp[code], typ)
		}
	}
}

// Or[Out, Sse[T]] answers one value or a stream, as the handler chooses, and
// the document publishes both at the one status.

type piece struct {
	Text string `json:"text"`
}

type eitherIn struct {
	Stream bool `json:"stream"`
}

func either(_ context.Context, in *eitherIn) (*zip.Or[piece, zip.Sse[piece]], error) {
	if !in.Stream {
		return &zip.Or[piece, zip.Sse[piece]]{A: &piece{Text: "whole"}}, nil
	}
	return &zip.Or[piece, zip.Sse[piece]]{B: &zip.Sse[piece]{Send: func(_ context.Context, emit func(zip.Event[piece]) error) error {
		for _, w := range []string{"a", "b"} {
			if err := emit(zip.Event[piece]{Data: piece{Text: w}}); err != nil {
				return err
			}
		}
		return emit(zip.Event[piece]{Text: "[DONE]"})
	}}}, nil
}

func TestOr_ValueOrStream(t *testing.T) {
	app := zip.New(zip.Config{AppName: "ask", DisableStartupMessage: true})
	app.Post("/v1/ask", either)

	code, body := call2(t, app, "POST", "/v1/ask", `{"stream":false}`)
	if code != 200 || body != `{"text":"whole"}` {
		t.Errorf("value: %d %s", code, body)
	}
	resp, err := app.Test(httptest.NewRequest("POST", "/v1/ask", strings.NewReader(`{"stream":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if want := "data: {\"text\":\"a\"}\n\ndata: {\"text\":\"b\"}\n\ndata: [DONE]\n\n"; string(b) != want ||
		resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("stream: %q as %q", b, resp.Header.Get("Content-Type"))
	}
	ok := opIn(t, app, "/v1/ask", "post")["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if _, has := ok["application/json"]; !has {
		t.Errorf("200 = %v, want the value", ok)
	}
	sse, _ := ok["text/event-stream"].(map[string]any)
	if ref, _ := sse["x-events"].(map[string]any)["$ref"].(string); !strings.HasSuffix(ref, "/piece") {
		t.Errorf("200 = %v, want the stream with its events' type", ok)
	}
}

// An event stream is text/event-stream frames, each flushed as it is sent; a
// quiet interval writes a comment so a proxy keeps the stream open.
func TestSse_FramesAndKeepAlive(t *testing.T) {
	app := zip.New(zip.Config{AppName: "sse", DisableStartupMessage: true})
	app.Get("/v1/events", func(context.Context, *struct{}) (*zip.Sse[piece], error) {
		return &zip.Sse[piece]{Keep: 10 * time.Millisecond, Send: func(_ context.Context, emit func(zip.Event[piece]) error) error {
			if err := emit(zip.Event[piece]{Event: "start", ID: "1", Retry: 500, Data: piece{Text: "x\ny"}}); err != nil {
				return err
			}
			time.Sleep(80 * time.Millisecond)
			return emit(zip.Event[piece]{Event: "a\nb", Text: "line1\r\nline2"})
		}}, nil
	})
	resp, err := app.Test(httptest.NewRequest("GET", "/v1/events", nil), zip.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	got := string(b)
	first := "event: start\nid: 1\nretry: 500\ndata: {\"text\":\"x\\ny\"}\n\n"
	last := "event: ab\ndata: line1\ndata: line2\n\n"
	if !strings.HasPrefix(got, first) || !strings.HasSuffix(got, last) || !strings.Contains(got, "\n:\n\n") {
		t.Errorf("stream = %q", got)
	}
	sse := opIn(t, app, "/v1/events", "get")["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["text/event-stream"]
	if sse == nil {
		t.Error("no text/event-stream in the document")
	}
}

// Over MCP an event stream is collected: its events, one JSON line each.
func TestSse_IsCollectedForMCP(t *testing.T) {
	app := zip.New(zip.Config{AppName: "ask", DisableStartupMessage: true})
	app.Post("/v1/ask", either)
	built(t, app)
	env := mcpCall(t, app, zip.ID("POST", "/v1/ask"), map[string]any{"stream": true})
	if got := mcpText(t, env["result"].(map[string]any)); got != "{\"text\":\"a\"}\n{\"text\":\"b\"}\n[DONE]\n" {
		t.Errorf("tool text = %q", got)
	}
}

// A request body may be a union too: a type whose OneOf method names its
// alternatives decodes itself and is published as oneOf.

type ingestEvent struct {
	Kind string `json:"kind"`
}

type ingestBatch struct {
	Events []ingestEvent `json:"events"`
}

type intake struct{ events []ingestEvent }

func (intake) OneOf() (ingestEvent, ingestBatch) { return ingestEvent{}, ingestBatch{} }

func (i *intake) UnmarshalJSON(b []byte) error {
	var batch ingestBatch
	if json.Unmarshal(b, &batch) == nil && batch.Events != nil {
		i.events = batch.Events
		return nil
	}
	var one ingestEvent
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	i.events = []ingestEvent{one}
	return nil
}

func TestOneOf_RequestUnion(t *testing.T) {
	app := zip.New(zip.Config{AppName: "in", DisableStartupMessage: true})
	app.Post("/v1/intake", func(_ context.Context, in *intake) (*mkOut, error) {
		return &mkOut{ID: strings.Repeat("e", len(in.events))}, nil
	})
	if _, body := call2(t, app, "POST", "/v1/intake", `{"events":[{"kind":"a"},{"kind":"b"}]}`); body != `{"id":"ee"}` {
		t.Errorf("batch: %s", body)
	}
	if _, body := call2(t, app, "POST", "/v1/intake", `{"kind":"a"}`); body != `{"id":"e"}` {
		t.Errorf("one: %s", body)
	}
	b, _ := json.Marshal(app.OpenAPISpec())
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	_ = json.Unmarshal(b, &doc)
	one, _ := doc.Components.Schemas["intake"]["oneOf"].([]any)
	if len(one) != 2 {
		t.Errorf("intake = %v, want oneOf its two alternatives", doc.Components.Schemas["intake"])
	}
}

// zip.Or decodes a request into the alternative the JSON is.
func TestOr_DecodesTheAlternative(t *testing.T) {
	var o zip.Or[ingestEvent, []ingestEvent]
	if err := json.Unmarshal([]byte(`[{"kind":"a"}]`), &o); err != nil || o.B == nil || o.A != nil {
		t.Errorf("list: %v %+v", err, o)
	}
	if err := json.Unmarshal([]byte(`{"kind":"a"}`), &o); err != nil || o.A == nil || o.B != nil {
		t.Errorf("one: %v %+v", err, o)
	}
	if err := json.Unmarshal([]byte(`7`), &o); err == nil {
		t.Errorf("a number decoded as %+v", o)
	}
}

// A component name is a valid one whatever the type's arguments.
func TestOr_ComponentNameIsPlain(t *testing.T) {
	app := zip.New(zip.Config{AppName: "u", DisableStartupMessage: true})
	app.Post("/v1/things", func(context.Context, *zip.Or[ingestEvent, []ingestEvent]) (*mkOut, error) { return nil, nil })
	b, _ := json.Marshal(app.OpenAPISpec())
	if strings.Contains(string(b), "[") && strings.Contains(string(b), `"Or[`) {
		t.Errorf("a schema is named with brackets: %s", b)
	}
	if !strings.Contains(string(b), `"#/components/schemas/OrIngestEventIngestEventList"`) {
		t.Errorf("document = %s", b)
	}
}
