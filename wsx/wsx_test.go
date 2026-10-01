package wsx_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"
)

// A plain GET to a WebSocket address — a crawler, a probe, a tab opened on the
// URL — is the client's mistake: 426 with the protocol to switch to, never the
// upgrader's handshake error surfacing as a server fault.
func TestAPlainRequestIsTold426(t *testing.T) {
	app := zip.New(zip.Config{AppName: "wsx", DisableStartupMessage: true})
	reached := false
	app.Raw(http.MethodGet, "/ws", wsx.Upgrade(func(*wsx.Conn) error {
		reached = true
		return nil
	}))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/ws", nil))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d (%s), want 426", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Upgrade"); got != "websocket" {
		t.Fatalf("Upgrade header = %q, want websocket", got)
	}
	if !strings.Contains(string(body), "WebSocket") {
		t.Fatalf("body does not say what to do: %s", body)
	}
	if reached {
		t.Fatal("the handler ran for a request that never upgraded")
	}
}
