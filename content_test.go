// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fasthttp/websocket"

	"github.com/zap-proto/zip"
)

// An op that takes the request body itself — bytes, a form, a Parser — takes
// its content: the Content-Encoding undone and held to the body limit, or a
// refusal before the handler runs. It never takes the text fiber puts in place
// of a body it could not decode, and a multipart form is never larger than the
// body that carried it.

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// contentIn takes the body as bytes; contentForm as a form with one part.
type contentIn struct {
	Body zip.Body `json:"body"`
}

type contentForm struct {
	Note string    `json:"note" form:"note"`
	Part *zip.File `json:"part" form:"part"`
}

type contentOut struct {
	Got  string `json:"got"`
	Size int    `json:"size"`
}

func contentApp(ran *atomic.Int32) *zip.App {
	app := zip.New(zip.Config{AppName: "content", DisableStartupMessage: true, BodyLimit: 1 << 16})
	app.Post("/v1/bytes", func(_ context.Context, in *contentIn) (*contentOut, error) {
		ran.Add(1)
		return &contentOut{Got: string(in.Body.Bytes), Size: len(in.Body.Bytes)}, nil
	})
	app.Post("/v1/form", func(_ context.Context, in *contentForm) (*contentOut, error) {
		ran.Add(1)
		out := &contentOut{Got: in.Note}
		if in.Part != nil {
			out.Size = len(in.Part.Bytes)
		}
		return out, nil
	}, zip.Consumes("application/x-www-form-urlencoded", "multipart/form-data"))
	return app
}

func send(t *testing.T, app *zip.App, path, media, coding string, body []byte) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", media)
	if coding != "" {
		req.Header.Set("Content-Encoding", coding)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestContent_ABodyIsDecodedOrRefused(t *testing.T) {
	var ran atomic.Int32
	app := contentApp(&ran)
	plain := []byte("the statement, as sent")

	code, body := send(t, app, "/v1/bytes", "text/plain", "gzip", gzipped(t, plain))
	if code != 200 || !strings.Contains(body, `"got":"the statement, as sent"`) {
		t.Errorf("gzip: %d %s, want the decoded bytes", code, body)
	}
	code, body = send(t, app, "/v1/bytes", "text/plain", "identity", plain)
	if code != 200 || !strings.Contains(body, `"got":"the statement, as sent"`) {
		t.Errorf("identity: %d %s, want the bytes as sent", code, body)
	}

	ran.Store(0)
	bomb := gzipped(t, bytes.Repeat([]byte{'a'}, 1<<20))
	for _, c := range []struct {
		name, coding string
		body         []byte
		code         int
	}{
		{"corrupt gzip", "gzip", []byte("not gzip at all"), 400},
		{"a coding nobody undoes", "compress", plain, 415},
		{"an unknown coding", "rot13", plain, 415},
		{"past the limit once decoded", "gzip", bomb, 413},
	} {
		code, body := send(t, app, "/v1/bytes", "text/plain", c.coding, c.body)
		if code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, code, body, c.code)
		}
	}
	if n := ran.Load(); n != 0 {
		t.Errorf("the handler ran %d times on a body it could not have", n)
	}
}

func TestContent_AFormIsReadFromItsDecodedContent(t *testing.T) {
	var ran atomic.Int32
	app := contentApp(&ran)

	code, body := send(t, app, "/v1/form", "application/x-www-form-urlencoded", "gzip", gzipped(t, []byte("note=kept")))
	if code != 200 || !strings.Contains(body, `"got":"kept"`) {
		t.Errorf("a gzip url-encoded form: %d %s, want its field", code, body)
	}

	form := func(part []byte) (string, []byte) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("note", "with a part")
		w, _ := mw.CreateFormFile("part", "p.bin")
		_, _ = w.Write(part)
		_ = mw.Close()
		return mw.FormDataContentType(), buf.Bytes()
	}
	media, small := form([]byte("small part"))
	code, body = send(t, app, "/v1/form", media, "gzip", gzipped(t, small))
	if code != 200 || !strings.Contains(body, `"got":"with a part"`) || !strings.Contains(body, `"size":10`) {
		t.Errorf("a gzip multipart form: %d %s, want its field and part", code, body)
	}

	// A small request that decodes to a part far past the limit is refused at
	// the limit, before any part is read and before the handler runs.
	ran.Store(0)
	media, big := form(bytes.Repeat([]byte{'z'}, 8<<20))
	code, body = send(t, app, "/v1/form", media, "gzip", gzipped(t, big))
	if code != 413 || ran.Load() != 0 {
		t.Errorf("a multipart bomb: %d %s, ran %d times; want 413 and no run", code, body, ran.Load())
	}
}

// bareIn names nothing; the relay answers whatever its upstream did.
type bareIn struct{}

type relayed struct {
	// Text is the upstream's answer.
	Text string `json:"text"`
}

// closer records whether the reader it stands for was closed.
type closer struct {
	io.Reader
	closed atomic.Bool
}

func (c *closer) Close() error { c.closed.Store(true); return nil }

// An answer refused after its handler ran — a status or a header the op did
// not declare — still closes its Reader: a relay's Reader is the upstream's
// body, and nothing else will.
func TestContent_ARefusedAnswerClosesItsReader(t *testing.T) {
	var last atomic.Pointer[closer]
	app := zip.New(zip.Config{AppName: "relay", DisableStartupMessage: true})
	app.Get("/v1/relay", func(context.Context, *bareIn) (*zip.Verbatim[relayed], error) {
		c := &closer{Reader: strings.NewReader(`{"text":"partial"}`)}
		last.Store(c)
		return &zip.Verbatim[relayed]{Body: zip.Body{Type: "application/json", Status: http.StatusPartialContent, Reader: c}}, nil
	})
	app.Get("/v1/header", func(context.Context, *bareIn) (*zip.Verbatim[relayed], error) {
		c := &closer{Reader: strings.NewReader(`{"text":"tagged"}`)}
		last.Store(c)
		return &zip.Verbatim[relayed]{Body: zip.Body{Type: "application/json", Header: map[string]string{"X-Undeclared": "1"}, Reader: c}}, nil
	})
	for _, path := range []string{"/v1/relay", "/v1/header"} {
		for _, method := range []string{"GET", "HEAD"} {
			resp, err := app.Test(httptest.NewRequest(method, path, nil))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode < 500 {
				t.Errorf("%s %s answered %d, want the refusal of an undeclared answer", method, path, resp.StatusCode)
			}
			if c := last.Load(); c == nil || !c.closed.Load() {
				t.Errorf("%s %s left the upstream body open", method, path)
			}
		}
	}
}

// A field of the graph is one JSON value in and one out. Every op that is not
// that is absent from the schema and refused by the executor without running,
// a cookie is never an argument, and [zip.Here] refuses a socket op before its
// handler runs.
func TestContent_TheGraphCarriesOnlyPlainOps(t *testing.T) {
	var ran atomic.Int32
	count := func() { ran.Add(1) }
	app := zip.New(zip.Config{AppName: "graph", DisableStartupMessage: true})
	app.Get("/v1/live", func(context.Context, *bareIn) (*zip.Socket[line], error) {
		count()
		return &zip.Socket[line]{Serve: func(*websocket.Conn) error { return nil }}, nil
	}, zip.WithOperationID("live"))
	app.Get("/v1/events", func(context.Context, *bareIn) (*zip.Sse[line], error) {
		count()
		return &zip.Sse[line]{}, nil
	}, zip.WithOperationID("events"))
	app.Get("/v1/file", func(context.Context, *bareIn) (*zip.Body, error) {
		count()
		return &zip.Body{Reader: strings.NewReader("x")}, nil
	}, zip.WithOperationID("file"))
	app.Get("/v1/away", func(context.Context, *bareIn) (*zip.Redirect, error) {
		count()
		return &zip.Redirect{To: "/"}, nil
	}, zip.WithOperationID("away"))
	app.Get("/v1/either", func(context.Context, *bareIn) (*zip.Or[relayed, contentOut], error) {
		count()
		return &zip.Or[relayed, contentOut]{A: &relayed{}}, nil
	}, zip.WithOperationID("either"))
	app.Post("/v1/upload", func(context.Context, *contentIn) (*contentOut, error) {
		count()
		return &contentOut{}, nil
	}, zip.WithOperationID("upload"))
	app.Get("/v1/me", func(_ context.Context, in *sessionIn) (*sessionOut, error) {
		return &sessionOut{Session: in.Session}, nil
	}, zip.WithOperationID("me"))
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}

	sdl := app.GraphQLSDL()
	for _, name := range []string{"live", "events", "file", "away", "either", "upload"} {
		if strings.Contains(sdl, "  "+name) {
			t.Errorf("the schema publishes %s, which is not one JSON value in and one out:\n%s", name, sdl)
		}
	}
	if !strings.Contains(sdl, "  me: SessionOut") {
		t.Errorf("the schema should publish me with no session argument:\n%s", sdl)
	}

	for _, q := range []string{"{ live { text } }", "{ events { text } }", "{ file { Type } }", "{ away { to } }", "{ either { text } }"} {
		g := app.GraphQL(context.Background(), zip.GraphRequest{Query: q})
		if len(g.Errors) == 0 {
			t.Errorf("%s resolved: %+v", q, g.Data)
		}
	}
	g := app.GraphQL(context.Background(), zip.GraphRequest{Query: `mutation { upload(body: "eA==") { size } }`})
	if len(g.Errors) == 0 {
		t.Errorf("upload resolved: %+v", g.Data)
	}
	g = app.GraphQL(context.Background(), zip.GraphRequest{Query: `{ me(session: "attacker-chosen") { session } }`})
	if len(g.Errors) == 0 {
		t.Errorf("a cookie was taken as an argument: %+v", g.Data)
	}
	if n := ran.Load(); n != 0 {
		t.Errorf("the graph ran %d handlers it does not carry", n)
	}

	_, err := zip.Here[bareIn, zip.Socket[line]](context.Background(), app, "live", &bareIn{})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotImplemented || ran.Load() != 0 {
		t.Errorf("Here on a socket op: %v, ran %d times; want 501 before the handler", err, ran.Load())
	}
}

type sessionIn struct {
	// Session is the browser's session cookie.
	Session string `json:"session" cookie:"session"`
}

type sessionOut struct {
	// Session is the session the op read.
	Session string `json:"session"`
}

// A url-encoded form binds the fields the op declares and no others, and one
// of more fields than Go's own query parser takes is refused before any is
// decoded or the handler runs. The refusal is cheap: a few kilobytes that
// decode to millions of fields cost the decode and nothing per field.
func TestContent_AFormKeepsItsFieldsAndIsBounded(t *testing.T) {
	var ran atomic.Int32
	app := contentApp(&ran)
	const form = "application/x-www-form-urlencoded"

	code, body := send(t, app, "/v1/form", form, "", []byte("other=x&note=kept&another=y"))
	if code != 200 || !strings.Contains(body, `"got":"kept"`) {
		t.Errorf("a form beside fields the op does not declare: %d %s", code, body)
	}
	at := strings.Repeat("a=1&", 9999) + "note=at the bound"
	if code, body = send(t, app, "/v1/form", form, "", []byte(at)); code != 200 || !strings.Contains(body, `"got":"at the bound"`) {
		t.Errorf("a form of exactly 10000 fields: %d %s, want it read", code, body)
	}
	ran.Store(0)
	past := strings.Repeat("a=1&", 10000) + "note=past"
	if code, body = send(t, app, "/v1/form", form, "", []byte(past)); code != 400 || ran.Load() != 0 {
		t.Errorf("a form of 10001 fields: %d %s, ran %d times; want 400 and no run", code, body, ran.Load())
	}

	big := zip.New(zip.Config{AppName: "flood", DisableStartupMessage: true})
	big.Post("/v1/form", func(_ context.Context, in *contentForm) (*contentOut, error) {
		ran.Add(1)
		return &contentOut{Got: in.Note}, nil
	}, zip.Consumes(form))
	flood := gzipped(t, bytes.Repeat([]byte("a&"), 2<<20-16))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	code, body = send(t, big, "/v1/form", form, "gzip", flood)
	runtime.ReadMemStats(&after)
	if code != 400 || ran.Load() != 0 {
		t.Errorf("a %d-byte flood of empty fields: %d %s, want 400 and no run", len(flood), code, body)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Errorf("refusing a %d-byte flood allocated %d MiB; it should cost the decode, not a value per field", len(flood), alloc>>20)
	}
}

// Every Content-Encoding line is a coding, as one comma-separated line is, and
// a chain longer than any client sends is refused rather than decoded layer by
// layer.
func TestContent_EveryCodingLineIsUndoneAndTheChainIsBounded(t *testing.T) {
	var ran atomic.Int32
	app := contentApp(&ran)
	plain := []byte("coded twice")
	twice := gzipped(t, gzipped(t, plain))
	lines := func(body []byte, codings ...string) (int, string) {
		req := httptest.NewRequest("POST", "/v1/bytes", bytes.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		for _, c := range codings {
			req.Header.Add("Content-Encoding", c)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := lines(twice, "gzip", "gzip"); code != 200 || !strings.Contains(body, `"got":"coded twice"`) {
		t.Errorf("two gzip lines: %d %s, want both undone", code, body)
	}
	ran.Store(0)
	if code, body := lines(gzipped(t, plain), "gzip", "rot13"); code != 415 || ran.Load() != 0 {
		t.Errorf("a second line naming a coding nobody undoes: %d %s, ran %d; want 415 and no run", code, body, ran.Load())
	}
	thrice := gzipped(t, twice)
	if code, body := send(t, app, "/v1/bytes", "text/plain", "gzip, gzip, gzip", thrice); code != 200 || !strings.Contains(body, `"got":"coded twice"`) {
		t.Errorf("three codings: %d %s, want all undone", code, body)
	}
	ran.Store(0)
	if code, body := send(t, app, "/v1/bytes", "text/plain", "gzip, gzip, gzip, gzip", gzipped(t, thrice)); code != 415 || ran.Load() != 0 {
		t.Errorf("four codings: %d %s, ran %d; want 415 and no run", code, body, ran.Load())
	}
}

// Over HTTP the server has read a multipart body as a form before any handler
// runs, so a Body field holds what a handler reading the request body holds:
// the same bytes, typed or not.
func TestContent_AMultipartBodyIsWhatAHandlerReads(t *testing.T) {
	var raw []byte
	app := zip.New(zip.Config{AppName: "multipart", DisableStartupMessage: true})
	app.Raw("POST", "/v1/raw", func(c *zip.Ctx) error {
		raw = append([]byte(nil), c.Body()...)
		return c.NoContent(204)
	})
	app.Post("/v1/typed", func(_ context.Context, in *contentIn) (*contentOut, error) {
		return &contentOut{Got: string(in.Body.Bytes)}, nil
	}, zip.Consumes("multipart/form-data"))

	const boundary = "b0undary"
	body := "a preamble\r\n--" + boundary + "\r\nContent-Disposition: form-data; name=\"note\"\r\nContent-Type: text/plain\r\nX-Signed: 1\r\n\r\nsigned\r\n--" + boundary + "--\r\nan epilogue"
	media := "multipart/form-data; boundary=" + boundary
	if code, _ := send(t, app, "/v1/raw", media, "", []byte(body)); code != 204 {
		t.Fatalf("raw handler answered %d", code)
	}
	code, got := send(t, app, "/v1/typed", media, "", []byte(body))
	var out contentOut
	if err := json.Unmarshal([]byte(got), &out); code != 200 || err != nil {
		t.Fatalf("typed op: %d %s", code, got)
	}
	if out.Got != string(raw) {
		t.Errorf("the typed op holds\n%q\na handler reading the body holds\n%q", out.Got, raw)
	}
}
