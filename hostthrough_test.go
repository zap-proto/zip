package zip_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// THE HOST A CHILD SEES IS THE HOST THE CALLER ASKED FOR — through a REAL
// mount, not a stubbed client. The unit test for forward() passes a request it
// built itself; this one goes through the server path, where fasthttp has
// already parsed the URI in order to route, which is the condition under which
// Request.Write re-derives the Host header from the URI.
func TestMountedChildSeesTheCallersHost(t *testing.T) {
	seen := make(chan string, 4)

	child := zip.New(zip.Config{AppName: "child", DisableStartupMessage: true})
	child.Get("/v1/child/who", func(c *zip.Ctx) error {
		seen <- c.Host()
		return c.String(200, "ok")
	})
	dir := sockDir(t)
	sock := dir + "/child.sock"
	go func() { _ = child.Listen(sock) }()
	waitSock(t, sock)

	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	loaded, err := zip.Load(zip.Plugin{Name: "child", Addr: sock}, "/v1/child")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	host.Use(loaded)

	// First establish what the HOST sees: if the harness does not deliver a Host,
	// the child cannot be blamed for not receiving one.
	host.Get("/echo-host", func(c *zip.Ctx) error { return c.String(200, "["+c.Host()+"]") })
	er := httptest.NewRequest(http.MethodGet, "/echo-host", nil)
	er.Host = "lux.id"
	eresp, eerr := host.Test(er)
	if eerr != nil {
		t.Fatalf("echo: %v", eerr)
	}
	ebuf := make([]byte, 64)
	en, _ := eresp.Body.Read(ebuf)
	_ = eresp.Body.Close()
	t.Logf("HOST saw: %s", string(ebuf[:en]))

	req := httptest.NewRequest(http.MethodGet, "/v1/child/who", nil)
	req.Host = "lux.id"
	resp, err := host.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case got := <-seen:
		if got != "lux.id" {
			t.Fatalf("child saw Host %q, want \"lux.id\" — a brand-resolving child cannot answer as the brand it was asked as", got)
		}
	default:
		t.Fatal("the child never served the request")
	}
}
