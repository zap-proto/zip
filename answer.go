// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
	"github.com/zap-proto/zip/internal/ws"
)

// The answers a typed op may give besides one JSON value. Each is the Out of an
// ordinary zip.Get/Post/…, so the op keeps its schema, its prose, its MCP tool,
// its CLI command and its place in every SDK, and the document says what the
// wire carries: bytes under their media type, an event stream and the type of
// one event, an upgrade and the type of one message, a redirect and its
// Location.

// Verbatim is a [Body] documented as T: a relay's answer, written exactly as
// the upstream sent it, while the document, the SDKs and the tool describe the
// upstream's 2xx shape T.
//
// Its status is the upstream's. A 2xx or 3xx must be one the op declared, as
// for any answer; a 4xx or 5xx passes through undeclared, because it is the
// upstream's refusal in the upstream's own words, and the document says so
// beside the op's own refusal. Verbatim[Sse[E]] relays an event stream and is
// documented as one whose events are E.
type Verbatim[T any] struct{ Body }

// documents is the type a Verbatim is documented as.
func (Verbatim[T]) documents() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

// Sse is an answer written as server-sent events (text/event-stream), one
// [Event] per frame, each written and flushed as Send produces it.
//
//	return &zip.Sse[Chunk]{Keep: 15 * time.Second, Send: func(ctx context.Context, emit func(zip.Event[Chunk]) error) error {
//	    for {
//	        select {
//	        case chunk, ok := <-chunks:
//	            if !ok {
//	                return emit(zip.Event[Chunk]{Text: "[DONE]"})
//	            }
//	            if err := emit(zip.Event[Chunk]{Data: chunk}); err != nil {
//	                return err // the client is gone
//	            }
//	        case <-ctx.Done():
//	            return ctx.Err()
//	        }
//	    }
//	}}, nil
//
// Send runs after the handler returns, once the status and headers are out, on
// a goroutine of its own. Its ctx ends when the stream does — a write found the
// client gone (so in a quiet stream only Keep finds it), or a tool call has
// collected all it will — and a producer that waits on anything selects on it. An error from emit means the stream
// has ended, and Send should return it. An error Send returns ends the stream
// where it stands: the status already went out, so it cannot be a refusal.
//
// The document publishes text/event-stream with the event's type as x-events.
// Over MCP and the CLI the events are the result, one JSON line each.
type Sse[T any] struct {
	// Send produces the events, calling emit once per event in order, until
	// it is done or ctx ends.
	Send func(ctx context.Context, emit func(Event[T]) error) error `zap:"-"`
	// Keep writes an empty comment line whenever this long passes with no
	// event, so a proxy keeps a waiting stream open and a client that left is
	// found by the write. Zero writes none.
	Keep time.Duration
	// Header are the answer's headers beside Content-Type, each one the op
	// declared ([WithResponseHeader]).
	Header map[string]string `zap:"-"`
}

// ResponseHeaders are the headers the stream states.
func (s Sse[T]) ResponseHeaders() map[string]string { return s.Header }

// frame is the type one event carries.
func (Sse[T]) frame() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

// Event is one server-sent event. Data is written as one line of JSON; Text,
// when it is not empty, is written in its place as it is (a sentinel such as
// [DONE] is text, not a value). A line break in Text, Event or ID cannot start
// a field of its own: Text becomes several data lines, Event and ID lose theirs.
type Event[T any] struct {
	// Event is the event's name, the field a browser dispatches on.
	Event string
	// ID is the id a reconnecting client sends back as Last-Event-ID.
	ID string
	// Retry is how long, in milliseconds, a client waits before reconnecting.
	Retry int
	// Data is the event's value.
	Data T
	// Text is the event's data as written, in place of Data.
	Text string
}

// payload is the event's data as the stream carries it.
func (e Event[T]) payload() ([]byte, error) {
	if e.Text != "" {
		return []byte(e.Text), nil
	}
	return jsonenc.Marshal(e.Data)
}

// encode is the event as the bytes text/event-stream carries.
func (e Event[T]) encode() ([]byte, error) {
	data, err := e.payload()
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if e.Event != "" {
		b.WriteString("event: " + oneLine(e.Event) + "\n")
	}
	if e.ID != "" {
		b.WriteString("id: " + oneLine(e.ID) + "\n")
	}
	if e.Retry > 0 {
		b.WriteString("retry: " + strconv.Itoa(e.Retry) + "\n")
	}
	for _, line := range sseLines(string(data)) {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

// keepLine is what a quiet interval writes: an empty comment, as zip in Rust
// writes it.
var keepLine = []byte(":\n\n")

// sseLines splits on every line break the event-stream format knows.
func sseLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func oneLine(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }

// events runs Send, handing each event to frame as the bytes the stream carries
// (wire) or as its data alone. Once ctx ends, emit answers errGone.
func (s *Sse[T]) events(ctx context.Context, wire bool, frame func([]byte) error) (err error) {
	if s.Send == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("zip: an event stream panicked: %v", r)
		}
	}()
	return s.Send(ctx, func(e Event[T]) error {
		if ctx.Err() != nil {
			return errGone
		}
		var b []byte
		var err error
		if wire {
			b, err = e.encode()
		} else {
			b, err = e.payload()
		}
		if err != nil {
			return err
		}
		return frame(b)
	})
}

// stream is how the REST door writes any event stream.
type stream interface {
	events(ctx context.Context, wire bool, frame func([]byte) error) error
	keepEvery() time.Duration
}

func (s *Sse[T]) keepEvery() time.Duration { return s.Keep }

// errGone is what emit answers once the stream has ended: the client stopped
// reading, or whoever was collecting it stopped.
var errGone = errors.New("zip: the client is gone")

// pump runs a stream's producer beside its sink and hands the sink each frame
// as it is produced — the bytes text/event-stream carries when wire, else the
// event's data alone — and keepLine whenever keep passes with none. It returns
// when the producer ends, the sink refuses a frame, or ctx ends; in the last two
// cases the producer's ctx is cancelled, so its next emit answers errGone.
func pump(ctx context.Context, s stream, wire bool, keep time.Duration, sink func([]byte) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	frames := make(chan []byte)
	ended := make(chan error, 1)
	go func() {
		ended <- s.events(ctx, wire, func(b []byte) error {
			select {
			case frames <- b:
				return nil
			case <-ctx.Done():
				return errGone
			}
		})
	}()
	var tick <-chan time.Time
	var timer *time.Timer
	if keep > 0 {
		timer = time.NewTimer(keep)
		defer timer.Stop()
		tick = timer.C
	}
	for {
		select {
		case b := <-frames:
			if err := sink(b); err != nil {
				return err
			}
			if timer != nil {
				timer.Reset(keep)
			}
		case <-tick:
			if err := sink(keepLine); err != nil {
				return err
			}
			timer.Reset(keep)
		case err := <-ended:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// writeEvents writes a stream to w, flushing each event, with a keep-alive
// comment in every quiet interval when the stream asks for one. A write that
// fails is the client gone, and ends the stream.
func writeEvents(ctx context.Context, s stream, w *bufio.Writer) error {
	return pump(ctx, s, true, s.keepEvery(), func(b []byte) error {
		if _, err := w.Write(b); err != nil {
			return errGone
		}
		if w.Flush() != nil {
			return errGone
		}
		return nil
	})
}

// Socket is an answer that upgrades the connection to a WebSocket whose
// messages are M. The op validates, authorizes and binds its input like any
// other; then the connection is upgraded and Serve runs on it until it
// returns.
//
//	return &zip.Socket[Frame]{Serve: func(conn *wsx.Conn) error { … }}, nil
//
// A request that cannot become a connection is refused before the handler
// runs, so a handler with effects runs only for a connection it will get: over
// ZAP, which carries no upgrade, 501; one that does not ask to upgrade, 426 with
// Upgrade: websocket; one asking for another protocol version, 426 with
// Sec-WebSocket-Version: 13; one with no Sec-WebSocket-Key, 400; and a browser
// on an origin the op does not admit ([Origins]), 403 — a socket op may read a
// cookie, which a browser sends cross-site too. The document publishes 101 with
// M as x-events and marks the op x-socket: websocket. A connection is not a
// call, so the op is no MCP tool and has no call-plane method; the CLI bridges
// it to stdin and stdout.
type Socket[M any] struct {
	// Serve runs on the upgraded connection.
	Serve func(conn *ws.Conn) error `zap:"-"`
	// Subprotocols are the protocols the op speaks, in its order of
	// preference; the first the client also offers is selected.
	Subprotocols []string `zap:"-"`
	// Compression negotiates per-message deflate when the client offers it.
	Compression bool `zap:"-"`
}

// Origins names the browser origins, besides the address's own, that may open
// an op's WebSocket: "https://app.example", or "*" for any. Without it only the
// address's own origin may; a request carrying no Origin is not a browser's
// and is always admitted. The handler runs only for an origin admitted.
func Origins(origins ...string) OpOption {
	return func(op *registeredOp) { op.Origins = append([]string(nil), origins...) }
}

// frame is the type one message carries.
func (Socket[M]) frame() reflect.Type { return reflect.TypeOf((*M)(nil)).Elem() }

// serve upgrades a connection the REST door already found upgradable.
func (s *Socket[M]) serve(c fiber.Ctx) error {
	serve := s.Serve
	cfg := ws.Config{
		Subprotocols:      s.Subprotocols,
		EnableCompression: s.Compression,
		// The origin was admitted before the handler ran ([upgradable]).
		CheckOrigin: func(*fasthttp.RequestCtx) bool { return true },
	}
	return ws.Upgrade(c.RequestCtx(), cfg, func(conn *ws.Conn) {
		if serve != nil {
			_ = serve(conn)
		}
	})
}

// upgradable refuses a request that cannot become a WebSocket the op will
// serve, before its handler runs: one carried over ZAP, which runs no upgrade
// (501); one that does not ask (426, with Upgrade: websocket); one asking for
// another protocol version (426, with the one spoken); one with no key (400);
// and a browser on an origin the op does not admit (403).
func upgradable(c fiber.Ctx, origins []string) error {
	rc := c.RequestCtx()
	if carriedByZAP(rc) {
		return Errorf(http.StatusNotImplemented, "%s %s upgrades to a WebSocket, which does not cross ZAP; connect over HTTP", c.Method(), c.Path())
	}
	if !ws.Is(rc) {
		c.Set("Upgrade", "websocket")
		return Errorf(http.StatusUpgradeRequired, ws.Refusal)
	}
	if !ws.Speaks(rc) {
		c.Set("Sec-WebSocket-Version", ws.Version)
		return Errorf(http.StatusUpgradeRequired, "this address speaks WebSocket version %s", ws.Version)
	}
	if len(rc.Request.Header.Peek("Sec-WebSocket-Key")) == 0 {
		return ErrBadRequest("a WebSocket handshake carries a Sec-WebSocket-Key")
	}
	if !ws.Admits(rc, origins) {
		return Errorf(http.StatusForbidden, "%s may not open a connection here", rc.Request.Header.Peek("Origin"))
	}
	return nil
}

// upgrader is a [Socket], whatever its message type.
type upgrader interface{ serve(c fiber.Ctx) error }

// NotACall is why an op that answers a [Socket] is no MCP tool and no call:
// the op's answer is a connection that stays open.
const NotACall = "a connection is not a call"

// Redirect is an answer that sends the client elsewhere: Status (a 3xx the op
// declared; zero is the first it declared, 302 when it declared none) with
// Location set to To and no body. The document publishes the status and its
// Location header; MCP and the CLI answer the location.
//
// A type that embeds Redirect is a redirect too, so one that also sets cookies
// is a struct embedding it with a Cookies method ([CookieCoder]): the sign-in
// leg that clears one cookie and sets another answers both.
type Redirect struct {
	// To is the location the client is sent to.
	To string `json:"to"`
	// Status is the redirect's status.
	Status int `json:"status,omitempty"`
}

// StatusCode is the status the redirect states; zero is the op's own.
func (r Redirect) StatusCode() int { return r.Status }

// redirect is the Redirect an answer is, promoted to a type that embeds one.
func (r *Redirect) redirect() *Redirect { return r }

// redirector is an answer that is a [Redirect], or embeds one.
type redirector interface{ redirect() *Redirect }

// CookieCoder is an answer that sets cookies: one Set-Cookie per cookie, which
// a header map cannot say. An op whose answer is one declares Set-Cookie by
// being one.
type CookieCoder interface{ Cookies() []*http.Cookie }

var cookieCoder = reflect.TypeOf((*CookieCoder)(nil)).Elem()

// Produces names the media types an op answers with when they are not
// application/json: application/scim+json, application/jwk-set+json, or the
// types of the bytes a [Body] answers. The first is what a JSON answer is sent
// as.
func Produces(media ...string) OpOption {
	if len(media) == 0 {
		panic("zip: Produces needs at least one media type")
	}
	return func(op *registeredOp) { op.Produces = append([]string(nil), media...) }
}

// Answer kinds a manifest names an op's stream by.
const (
	streamSSE    = "sse"
	streamBytes  = "bytes"
	streamSocket = "socket"
)

// answer is what an op's Out may put on the wire, read once from its type. A
// union ([Or], or a type with a OneOf method) is flattened into the kinds its
// alternatives are.
type answer struct {
	// json are the JSON values the op may answer: one, or several when it is a
	// union. union is the union's own type when it is one that writes itself
	// (a OneOf method); an [Or]'s alternatives are flattened into json.
	json  []reflect.Type
	union reflect.Type
	// stream is "sse", "bytes" or "socket" when the op may answer one, and
	// frame the type of one event or message.
	stream string
	frame  reflect.Type
	// verbatim says the answer is bytes relayed as sent, documented as json.
	verbatim bool
	// redirect says the op may answer a [Redirect].
	redirect bool
	// cookies says the answer may set cookies ([CookieCoder]).
	cookies bool
	// statuses are the statuses the alternatives state for themselves, by type.
	statuses map[reflect.Type]int
}

// answerOf reads an Out type's answer kinds.
func answerOf(out reflect.Type) answer {
	a := answer{statuses: map[reflect.Type]int{}}
	if out == nil {
		return a
	}
	a.add(out, true)
	return a
}

func (a *answer) add(t reflect.Type, top bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(cookieCoder) || reflect.PointerTo(t).Implements(cookieCoder) {
		a.cookies = true
	}
	v := reflect.New(t).Interface()
	switch x := v.(type) {
	case interface{ documents() reflect.Type }:
		a.verbatim = true
		d := x.documents()
		if f, ok := reflect.New(d).Interface().(interface{ frame() reflect.Type }); ok {
			if _, sse := reflect.New(d).Interface().(stream); sse {
				a.setStream(streamSSE, f.frame())
				return
			}
		}
		a.json = append(a.json, d)
		return
	case stream:
		a.setStream(streamSSE, v.(interface{ frame() reflect.Type }).frame())
		return
	case upgrader:
		a.setStream(streamSocket, v.(interface{ frame() reflect.Type }).frame())
		return
	case answerBody:
		a.setStream(streamBytes, nil)
		return
	case redirector:
		a.redirect = true
		return
	}
	if alts := alternatives(t); len(alts) > 0 {
		if !isOr(t) {
			// A union of JSON values decoded by its own type: each
			// alternative is a schema, the value is written as it marshals.
			a.union = t
			for _, alt := range alts {
				a.json = append(a.json, alt)
				a.state(alt)
			}
			return
		}
		for _, alt := range alts {
			a.add(alt, false)
		}
		return
	}
	a.json = append(a.json, t)
	if !top {
		a.state(t)
	}
}

// state records the status an alternative states for itself: the value
// StatusCode of its zero value.
func (a *answer) state(t reflect.Type) {
	if sc, ok := reflect.Zero(t).Interface().(StatusCoder); ok {
		if code := sc.StatusCode(); code != 0 {
			a.statuses[t] = code
		}
	}
}

func (a *answer) setStream(kind string, frame reflect.Type) {
	if a.stream != "" && a.stream != kind {
		panic(fmt.Sprintf("zip: an answer may be one kind of stream, not both %s and %s", a.stream, kind))
	}
	a.stream, a.frame = kind, frame
}

// settle completes an op's declaration from its answer: the statuses its
// alternatives and its redirects state, and the headers a redirect and a cookie
// set, so the document and the seam agree on them without the author repeating
// what the type already says.
func (a answer) settle(op *registeredOp) {
	if a.stream == streamSocket {
		if op.Method != http.MethodGet {
			panic(fmt.Sprintf("zip: %s %s answers a WebSocket, which a client opens with GET", op.Method, op.Path))
		}
		if len(a.json) > 0 || a.redirect {
			panic(fmt.Sprintf("zip: %s %s answers a WebSocket and something else; an upgrade is the whole answer", op.Method, op.Path))
		}
	}
	declared := len(op.Statuses) > 0
	primary := 200
	if declared {
		primary = op.Statuses[0]
	}
	want := []int{}
	for _, code := range a.statuses {
		want = append(want, code)
	}
	if a.redirect {
		has := false
		for _, code := range op.Statuses {
			if code >= 300 && code < 400 {
				has = true
			}
		}
		if !has {
			want = append(want, http.StatusFound)
		}
		op.ResponseHeaders = addHeader(op.ResponseHeaders, "Location")
	}
	if a.cookies {
		op.ResponseHeaders = addHeader(op.ResponseHeaders, "Set-Cookie")
	}
	if a.stream == streamBytes || a.verbatim {
		op.ResponseHeaders = addHeader(op.ResponseHeaders, "Content-Disposition")
	}
	for _, code := range want {
		if containsInt(op.Statuses, code) {
			continue
		}
		if declared && !(a.redirect && code == http.StatusFound) {
			panic(fmt.Sprintf("zip: %s %s answers %d, which its WithStatus does not declare", op.Method, op.Path, code))
		}
		if len(op.Statuses) == 0 && (len(a.json) > 0 || a.stream != "") {
			op.Statuses = append(op.Statuses, primary)
		}
		op.Statuses = append(op.Statuses, code)
	}
}

func addHeader(list []string, name string) []string {
	for _, h := range list {
		if strings.EqualFold(h, name) {
			return list
		}
	}
	return append(list, name)
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// bodyOf is the [Body] an answer carries, when it carries one: a Body, a
// Verbatim, or any type that embeds a Body.
func bodyOf(out any) (*Body, bool) {
	if b, ok := out.(answerBody); ok {
		if v := reflect.ValueOf(out); v.Kind() == reflect.Pointer && !v.IsNil() {
			return b.body(), true
		}
	}
	return nil, false
}

// unwrap is the alternative a union answer holds, or the answer itself.
func unwrap(out any) any {
	for {
		u, ok := out.(interface{ either() any })
		if !ok {
			return out
		}
		out = u.either()
	}
}

// writeAnswer writes an op's answer over REST. It is the one place the kinds
// above reach the wire.
func writeAnswer(c fiber.Ctx, op *registeredOp, out any) error {
	out = unwrap(out)
	if out == nil {
		code, err := statusOf(op, nil, false)
		if err != nil {
			return err
		}
		c.Status(nonZero(code, http.StatusNoContent))
		return nil
	}
	if s, ok := out.(upgrader); ok {
		return s.serve(c)
	}
	b, isBody := bodyOf(out)
	hdrs, err := responseHeadersOf(op, out)
	if err != nil {
		return err
	}
	code, err := statusOf(op, out, op.ans.verbatim)
	if err != nil {
		return err
	}
	if cc, ok := out.(CookieCoder); ok {
		for _, ck := range cc.Cookies() {
			if ck == nil {
				continue
			}
			if v := ck.String(); v != "" {
				c.Response().Header.Add("Set-Cookie", v)
			}
		}
	}
	for name, v := range hdrs {
		c.Set(name, v)
	}
	switch x := out.(type) {
	case redirector:
		c.Set("Location", x.redirect().To)
		c.Status(nonZero(code, http.StatusFound))
		return nil
	case stream:
		if code != 0 {
			c.Status(code)
		}
		c.Set(fiber.HeaderContentType, "text/event-stream")
		// The stream outlives the handler, so its ctx is its own: it ends when a
		// write finds the client gone, or when the producer is done. A quiet
		// stream finds a client that left only through Keep.
		return c.SendStreamWriter(func(w *bufio.Writer) {
			defer func() { _ = recover() }()
			_ = writeEvents(context.Background(), x, w)
		})
	}
	if code != 0 {
		c.Status(code)
	}
	if isBody {
		// No bytes and no stated type is no Content-Type: a 304 or an empty
		// answer carries none, as a handler that wrote nothing never did.
		media := b.Type
		if media == "" && (len(b.Bytes) > 0 || b.Reader != nil) {
			media = produces(op, mimeOctet)
		}
		if media != "" {
			c.Set(fiber.HeaderContentType, media)
		}
		if b.Name != "" {
			c.Set(fiber.HeaderContentDisposition, disposition(b.Name))
		}
		if b.Reader != nil {
			r := b.Reader
			return c.SendStreamWriter(func(w *bufio.Writer) { copyFlushing(w, r) })
		}
		return c.Send(b.Bytes)
	}
	// The framework's own JSON media (with its charset) unless the op names
	// another for its JSON: an op that declares nothing answers as it always
	// has, and one whose Produces names the media of its bytes keeps JSON for
	// its JSON.
	if media := produces(op, mimeJSON); media != mimeJSON {
		return c.JSON(out, media)
	}
	return c.JSON(out)
}

// disposition is the Content-Disposition that saves an answer as name: the
// name quoted, its quote and backslash escaped, and any control character —
// which could end the header and start another — dropped.
func disposition(name string) string {
	var b strings.Builder
	b.WriteString(`attachment; filename="`)
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case r == '"' || r == '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// copyFlushing writes r to w a read at a time, flushing each, so bytes reach
// the client as the source yields them. It closes r when it can. It runs on the
// server's stream goroutine, where nothing above it recovers, so a reader that
// panics ends the answer where it stands instead of the process.
func copyFlushing(w *bufio.Writer, r io.Reader) {
	defer func() { _ = recover() }()
	if c, ok := r.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if w.Flush() != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// produces is the media a kind of answer is sent as: the op's first Produces,
// else the kind's own.
func produces(op *registeredOp, dflt string) string {
	if len(op.Produces) > 0 && (dflt == mimeJSON) == (op.ans.stream != streamBytes) {
		return op.Produces[0]
	}
	return dflt
}

func nonZero(code, dflt int) int {
	if code == 0 {
		return dflt
	}
	return code
}

// toolBound is the most a tool result carries of an answer that is bytes or a
// stream. A tool result is read into a model's context in one piece, so an
// answer past it is cut at a frame or at the bound and says so.
const toolBound = 1 << 20

// errBound ends a collection at the tool bound.
var errBound = errors.New("zip: the tool result bound was reached")

// toolWait is the longest a tool call collects a stream for: under the
// sixty-second request timeout the reference MCP clients default to, so the
// answer arrives while someone is still waiting for it.
const toolWait = 45 * time.Second

// collect reads an answer that is not one JSON value into what a call returns:
// bytes with their media type, or a stream's events as JSON lines, bounded in
// bytes and a stream in time as well. truncated says a bound was reached
// first; the producer of a stream cut short sees its ctx end.
func collect(ctx context.Context, out any) (data []byte, media string, truncated bool, err error) {
	if s, ok := out.(stream); ok {
		var b []byte
		ctx, cancel := context.WithTimeout(ctx, toolWait)
		defer cancel()
		err = pump(ctx, s, false, 0, func(frame []byte) error {
			if len(b)+len(frame)+1 > toolBound {
				return errBound
			}
			b = append(append(b, frame...), '\n')
			return nil
		})
		if errors.Is(err, errBound) || errors.Is(err, context.DeadlineExceeded) {
			return b, "application/jsonl", true, nil
		}
		return b, "application/jsonl", false, err
	}
	b, _ := bodyOf(out)
	if b.Reader == nil {
		if len(b.Bytes) > toolBound {
			return b.Bytes[:toolBound], b.Type, true, nil
		}
		return b.Bytes, b.Type, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, toolWait)
	defer cancel()
	data, truncated, err = readBounded(ctx, b.Reader)
	return data, b.Type, truncated, err
}

// readBounded reads r to its end, toolBound bytes or ctx's end, whichever
// comes first, and closes it when it can. The read runs beside the wait, so a
// reader that never yields cannot hold the call; closing it is what ends a read
// left blocked.
func readBounded(ctx context.Context, r io.Reader) (data []byte, truncated bool, err error) {
	c, closes := r.(io.Closer)
	if closes {
		defer func() { _ = c.Close() }()
	}
	chunks := make(chan []byte)
	ended := make(chan error, 1)
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				select {
				case chunks <- append([]byte(nil), buf[:n]...):
				case <-ctx.Done():
					return
				}
			}
			if rerr != nil {
				if rerr == io.EOF {
					rerr = nil
				}
				ended <- rerr
				return
			}
		}
	}()
	for {
		select {
		case chunk := <-chunks:
			if len(data)+len(chunk) > toolBound {
				return append(data, chunk[:toolBound-len(data)]...), true, nil
			}
			data = append(data, chunk...)
		case err := <-ended:
			return data, false, err
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return data, true, nil
			}
			return data, false, ctx.Err()
		}
	}
}

// textual reports whether bytes of a media type read as text.
func textual(media string) bool {
	m := strings.ToLower(strings.TrimSpace(strings.Split(media, ";")[0]))
	switch {
	case m == "", strings.HasPrefix(m, "text/"):
		return true
	case strings.HasSuffix(m, "+json"), strings.HasSuffix(m, "+xml"), strings.HasSuffix(m, "+yaml"):
		return true
	}
	switch m {
	case mimeJSON, "application/jsonl", "application/x-ndjson", "application/xml", "application/yaml",
		"application/x-yaml", "application/javascript", "application/graphql", mimeForm:
		return true
	}
	return false
}

// toolContent is an answer that is not one JSON value, as MCP tool content:
// text as text, an image as an image, any other bytes as an embedded resource
// whose blob is the base64 of them.
func toolContent(ctx context.Context, id string, out any) (map[string]any, error) {
	data, media, truncated, err := collect(ctx, out)
	if err != nil {
		return nil, err
	}
	var content map[string]any
	switch {
	case textual(media):
		content = map[string]any{"type": "text", "text": string(data)}
	case strings.HasPrefix(media, "image/"):
		content = map[string]any{"type": "image", "data": base64Text(data), "mimeType": media}
	default:
		content = map[string]any{"type": "resource", "resource": map[string]any{
			"uri": "zip:" + id, "mimeType": media, "blob": base64Text(data),
		}}
	}
	result := map[string]any{"content": []map[string]any{content}}
	if truncated {
		result["_meta"] = map[string]any{"truncated": true, "bound": toolBound}
	}
	return result, nil
}
