package zip

import (
	"io"
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
	once   sync.Once
	ready  chan struct{}
}

func (w *streamWriter) Header() http.Header { return w.hdr }

func (w *streamWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.status = code
		close(w.ready)
	})
}

func (w *streamWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK) // net/http's implicit 200, same rule
	return w.pw.Write(b)
}

// Flush satisfies http.Flusher, which every streaming handler asserts before it
// will emit a frame.
func (w *streamWriter) Flush() { w.WriteHeader(http.StatusOK) }

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
