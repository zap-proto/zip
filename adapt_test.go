package zip_test

import (
	"io"
	"net/http"
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
