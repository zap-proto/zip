package zip_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestAdaptNetHTTP_HandlerFunc pins the ONE way to adapt a bare
// func(http.ResponseWriter, *http.Request): convert it with http.HandlerFunc,
// which IS an http.Handler, and hand that to AdaptNetHTTP. There is no
// separate func-shaped adapter — one adapter, one path.
func TestAdaptNetHTTP_HandlerFunc(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/fn", zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "from-func "+r.URL.Path)
	})))

	if status, body := call(t, app, "GET", "/fn", ""); status != 200 || !strings.Contains(body, "from-func /fn") {
		t.Fatalf("GET /fn: status=%d body=%q, want 200 with the func's output", status, body)
	}
}

// AN ADAPTED HANDLER GETS net/http's CONTENT SNIFFING, because adapting an
// http.Handler means adopting its contract and sniffing is part of it. A
// handler that writes an HTML page and sets no header is idiomatic stdlib —
// docdb's debug index is exactly that — and without this it answered
// text/plain, so a browser showed the markup instead of the page.
func TestAdaptNetHTTP_SniffsLikeNetHTTP(t *testing.T) {
	for _, c := range []struct {
		name, set, body, want string
	}{
		{"html is sniffed", "", "<html><body>hi</body></html>", "text/html; charset=utf-8"},
		{"text is sniffed", "", "just words", "text/plain; charset=utf-8"},
		{"an explicit type wins", "application/json", `{"a":1}`, "application/json"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := zip.New(zip.Config{AppName: "sniff", DisableStartupMessage: true})
			a.Get("/page", zip.AdaptNetHTTP(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				if c.set != "" {
					rw.Header().Set("Content-Type", c.set)
				}
				_, _ = rw.Write([]byte(c.body))
			})))
			if err := a.Build(); err != nil {
				t.Fatalf("build: %v", err)
			}
			resp, err := a.Fiber().Test(httptest.NewRequest("GET", "/page", nil))
			if err != nil {
				t.Fatalf("GET /page: %v", err)
			}
			if got := resp.Header.Get("Content-Type"); got != c.want {
				t.Errorf("Content-Type %q, want %q", got, c.want)
			}
		})
	}
}

// A handler that sets Content-Type to the EMPTY string is asking for no
// sniffing and no header — net/http's rule, and the one a caller reaches for
// when it is serving bytes it refuses to characterise.
func TestAdaptNetHTTP_EmptyTypeSuppressesSniffing(t *testing.T) {
	a := zip.New(zip.Config{AppName: "nosniff", DisableStartupMessage: true})
	a.Get("/raw", zip.AdaptNetHTTP(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header()["Content-Type"] = nil
		rw.Header()["Content-Type"] = []string{}
		_, _ = rw.Write([]byte("<html>not html to us</html>"))
	})))
	if err := a.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	resp, err := a.Fiber().Test(httptest.NewRequest("GET", "/raw", nil))
	if err != nil {
		t.Fatalf("GET /raw: %v", err)
	}
	if got := resp.Header.Get("Content-Type"); strings.Contains(got, "html") {
		t.Errorf("sniffed %q despite the handler declining to characterise the body", got)
	}
}
