package zip

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/zap-proto/fiber/v3/middleware/adaptor"
)

// A MOUNTED HANDLER STREAMS.
//
// fiber's adaptor hands the stdlib handler a ResponseWriter that collects the
// whole body and copies it out once the handler returns. For an ordinary reply
// that is only a buffer. For server-sent events it is the difference between a
// stream and one delivery at the end: a caller watching tokens arrive sees
// nothing until the answer is already complete, which is indistinguishable from
// a slow model and is why "streaming works" can be true and useless at once.
//
// So the handler runs against a pipe. The pipe is SYNCHRONOUS — a Write does not
// return until the reader has taken the bytes — which is what makes Flush honest
// here rather than a stub: by the time a handler calls it, what it wrote is
// already on its way and there is nothing withheld to push.
//
// Status and headers are read from the handler's FIRST act (WriteHeader, the
// implicit 200 on first Write, or simply returning) and applied before the body
// streams, because fasthttp writes them once and cannot revise them after.
type streamWriter struct {
	hdr    http.Header
	status int
	pw     *io.PipeWriter
	code   sync.Once // the status is the FIRST WriteHeader's
	commit sync.Once // the head is final once: at the first write, or at return
	ready  chan struct{}
}

func (w *streamWriter) Header() http.Header { return w.hdr }

// WriteHeader records the status and nothing more. net/http finalises the head
// at the first body write rather than here, and that is not an accident of its
// implementation — it is what lets it sniff a Content-Type the handler did not
// set. Releasing the head on this call would commit it before there was a body
// to sniff.
func (w *streamWriter) WriteHeader(code int) { w.code.Do(func() { w.status = code }) }

// commit finalises the head. b is the first body bytes, or nil when the handler
// returned or flushed without writing any.
func (w *streamWriter) commitHead(b []byte) {
	w.commit.Do(func() {
		// net/http sniffs the first write when the handler set no Content-Type,
		// and handlers rely on it: a debug index that writes an HTML page and
		// sets no header arrived as text/plain, so a browser showed the markup
		// instead of the page. Adapting an http.Handler means adopting its
		// contract, and this is part of it.
		//
		// A Content-Type that is PRESENT BUT EMPTY means the handler asked for
		// no sniffing — also net/http's rule — so the test is presence, not
		// emptiness.
		if _, set := w.hdr["Content-Type"]; !set && len(b) > 0 {
			w.hdr.Set("Content-Type", http.DetectContentType(b))
		}
		close(w.ready)
	})
}

func (w *streamWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK) // net/http's implicit 200, same rule
	// Before the pipe write, never after: the reader is blocked on the head and
	// the pipe is synchronous, so writing first would deadlock.
	w.commitHead(b)
	return w.pw.Write(b)
}

// Flush satisfies http.Flusher, which every streaming handler asserts before it
// will emit a frame. Nothing has been written yet, so there is nothing to sniff.
func (w *streamWriter) Flush() {
	w.WriteHeader(http.StatusOK)
	w.commitHead(nil)
}

func adaptStreaming(h http.Handler) func(*Ctx) error {
	return func(c *Ctx) error {
		req, err := adaptor.ConvertRequest(c.fc, false)
		if err != nil {
			return err
		}
		// The request outlives this call — the handler runs while the body is
		// still streaming — so it carries the connection's context rather than
		// the pooled fiber one, and a client that goes away cancels it.
		req = req.WithContext(c.fc.RequestCtx())

		// THE REQUEST-TARGET, AS THE CALLER SENT IT. ConvertRequest fills URL and
		// leaves this empty, and net/http's own server always sets it — so a
		// handler that reads it, rather than URL.Path, was handed nothing. An
		// empty target normalises to "/", so a relay asks its runtime for a path
		// nobody requested and answers 404 to every valid call. http.ServeMux
		// matches on URL.Path, which is why a mux in front of it looks fine.
		req.RequestURI = string(c.fc.Request().Header.RequestURI())

		// AN UPGRADE IS ANSWERED ON THE CONNECTION ITSELF. A net/http WebSocket
		// server (coder/websocket, gorilla) takes the socket with http.Hijacker and
		// writes the 101 itself; a writer without Hijack makes it answer 501 to
		// every upgrade. So the handler runs inside fasthttp's hijack, against the
		// raw connection, after this returns.
		if upgrading(c.fc.Request()) {
			serveUpgrade(c, h, req)
			return nil
		}

		pr, pw := io.Pipe()
		w := &streamWriter{hdr: make(http.Header), status: http.StatusOK, pw: pw, ready: make(chan struct{})}

		go func() {
			// A panicking handler must close the pipe, or the reader below
			// blocks forever holding the connection open.
			defer func() {
				if r := recover(); r != nil {
					_ = pw.CloseWithError(http.ErrAbortHandler)
				} else {
					_ = pw.Close()
				}
				w.WriteHeader(http.StatusOK) // a handler that wrote nothing still answered
				w.commitHead(nil)
			}()
			h.ServeHTTP(w, req)
		}()

		<-w.ready

		resp := c.fc.Response()
		resp.SetStatusCode(w.status)
		size := -1 // chunked unless the handler stated a length
		for k, vs := range w.hdr {
			if k == "Content-Length" {
				if n, convErr := strconv.Atoi(vs[0]); convErr == nil {
					size = n
				}
				continue // fasthttp writes this itself from the stream size
			}
			for _, v := range vs {
				resp.Header.Add(k, v)
			}
		}
		resp.SetBodyStream(pr, size)
		return nil
	}
}

// serveUpgrade runs h on the hijacked connection. The request no longer has the
// fasthttp context to carry — the handler must not hold ctx members past the
// hijack — so it carries one of its own, cancelled when the handler returns.
func serveUpgrade(c *Ctx, h http.Handler, req *http.Request) {
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	rc := c.fc.RequestCtx()
	rc.HijackSetNoResponse(true)
	rc.Hijack(func(conn net.Conn) {
		defer cancel()
		w := &upgradeWriter{
			conn: conn,
			brw:  bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)),
			hdr:  make(http.Header),
		}
		h.ServeHTTP(w, req)
		if !w.taken && !w.switched {
			w.answer()
		}
	})
}

// upgradeWriter is the ResponseWriter of an upgrade request. A handler that
// takes the connection (Hijack) speaks the new protocol on it; one that declines
// — an unauthorized socket is refused before it opens — has its status, headers
// and body written as an ordinary HTTP/1.1 reply, and the connection closes.
type upgradeWriter struct {
	conn     net.Conn
	brw      *bufio.ReadWriter
	hdr      http.Header
	status   int
	body     bytes.Buffer
	taken    bool
	switched bool // a 101 went out: the reply is the handler's from here
}

func (w *upgradeWriter) Header() http.Header { return w.hdr }

// WriteHeader records a final status and SENDS an informational one, as net/http
// does: coder/websocket writes its 101 with WriteHeader and only then hijacks, so
// a 101 that waited for the handler to return would never reach the client and
// the handshake would hang with the socket open.
func (w *upgradeWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && !w.taken {
		fmt.Fprintf(w.brw, "HTTP/1.1 %d %s\r\n", code, http.StatusText(code))
		_ = w.hdr.Write(w.brw)
		_, _ = w.brw.WriteString("\r\n")
		_ = w.brw.Flush()
		w.switched = w.switched || code == http.StatusSwitchingProtocols
		return
	}
	if w.status == 0 {
		w.status = code
	}
}

func (w *upgradeWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(b)
}

// Flush satisfies http.Flusher; nothing is sent until the handler returns or
// takes the connection.
func (w *upgradeWriter) Flush() {}

// Hijack hands the handler the connection, and with it the whole reply.
func (w *upgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.taken {
		return nil, nil, http.ErrHijacked
	}
	w.taken = true
	return w.conn, w.brw, nil
}

func (w *upgradeWriter) answer() {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w.brw, "HTTP/1.1 %d %s\r\n", w.status, http.StatusText(w.status))
	w.hdr.Set("Content-Length", strconv.Itoa(w.body.Len()))
	w.hdr.Set("Connection", "close")
	_ = w.hdr.Write(w.brw)
	_, _ = w.brw.WriteString("\r\n")
	_, _ = w.brw.Write(w.body.Bytes())
	_ = w.brw.Flush()
}
