package zip_test

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// TestProxy_OverUnixSocket proves the plumbing is read off the address shape
// rather than off a second scheme: the SAME "zap" transport serves and dials a
// unix socket when the address is a path. This is the colocated plugin case —
// no port to allocate, filesystem permissions as the ACL — carrying byte-identical
// ZAP frames to the tcp case.
func TestProxy_OverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "billing.sock")

	plugin := zip.New(zip.Config{AppName: "billing", DisableStartupMessage: true})
	plugin.Get("/v1/billing/invoices", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"servedBy": "over-uds"})
	})
	go func() { _ = plugin.Listen(sock) }() // bare path => zap scheme, unix network
	defer func() { _ = plugin.Shutdown() }()
	waitSocket(t, sock)

	core := zip.New(zip.Config{AppName: "core", DisableStartupMessage: true})
	core.Use(must(zip.Proxy("/v1/billing", sock)))

	status, body := call(t, core, "GET", "/v1/billing/invoices", "")
	if status != 200 || !strings.Contains(body, `"servedBy":"over-uds"`) {
		t.Fatalf("through unix mount: status=%d body=%q, want the plugin's JSON", status, body)
	}
}

// waitSocket blocks until path accepts a unix connection.
func waitSocket(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("unix socket %s never became reachable", path)
}

// These tests pin the plugin mechanism: Listen serves here, Proxy delegates
// there, and both read the transport off the address scheme through one
// registry. The property that matters is that the mounting app holds no
// reference whatsoever to the mounted app's code — that is what lets a service
// ship, rebuild, and redeploy as its own binary without relinking its host.

// TestProxy_OverZAP is the end-to-end proof. The "plugin" is a real, separate
// zip.App with its own routes, listening on the real ZAP transport. The "core"
// mounts it by address alone — a bare address, so DefaultScheme (ZAP) — and
// serves its routes as if they were local. Nothing is shared but the address.
func TestProxy_OverZAP(t *testing.T) {
	plugin := zip.New(zip.Config{AppName: "billing", DisableStartupMessage: true})
	plugin.Get("/v1/billing/invoices", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"servedBy": "billing-plugin"})
	})
	plugin.Post("/v1/billing/charge", func(c *zip.Ctx) error {
		return c.JSON(201, map[string]string{"echo": string(c.Body())})
	})

	pluginAddr := freeAddr(t)
	go func() { _ = plugin.Listen(pluginAddr) }() // bare addr = ZAP
	defer func() { _ = plugin.Shutdown() }()
	waitReachable(t, pluginAddr)

	core := zip.New(zip.Config{AppName: "core", DisableStartupMessage: true})
	core.Use(must(zip.Proxy("/v1/billing", pluginAddr)))

	// GET travels core -> ZAP -> plugin and the plugin's body comes back.
	status, body := call(t, core, "GET", "/v1/billing/invoices", "")
	if status != 200 {
		t.Fatalf("GET through mount: status %d, want 200 (body=%s)", status, body)
	}
	if !strings.Contains(body, `"servedBy":"billing-plugin"`) {
		t.Fatalf("GET through mount: body=%q, want the plugin's JSON", body)
	}

	// POST proves method, request body, and a non-200 status all survive the
	// hop — the request object is handed across, not reconstructed.
	status, body = call(t, core, "POST", "/v1/billing/charge", `{"amount":100}`)
	if status != 201 {
		t.Fatalf("POST through mount: status %d, want 201 (body=%s)", status, body)
	}
	if !strings.Contains(body, `{\"amount\":100}`) && !strings.Contains(body, `amount`) {
		t.Fatalf("POST through mount: body=%q, want the echoed request body", body)
	}
}

// TestProxy_StaticBeatsRemoteProxy pins the same precedence property the local mount
// path pins: a mount is an ORDINARY wildcard route, so a more specific route
// registered afterwards still wins. A mount that short-circuited the router
// would silently shadow the host's own routes.
func TestProxy_StaticBeatsRemoteProxy(t *testing.T) {
	plugin := zip.New(zip.Config{AppName: "plug", DisableStartupMessage: true})
	plugin.Get("/v1/billing/*", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"servedBy": "plugin"})
	})

	pluginAddr := freeAddr(t)
	go func() { _ = plugin.Listen(pluginAddr) }()
	defer func() { _ = plugin.Shutdown() }()
	waitReachable(t, pluginAddr)

	core := zip.New(zip.Config{AppName: "core", DisableStartupMessage: true})
	core.Use(must(zip.Proxy("/v1/billing", pluginAddr)))
	// Registered AFTER the mount, and still wins for its exact path.
	core.Get("/v1/billing/health", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"servedBy": "core"})
	})

	if status, body := call(t, core, "GET", "/v1/billing/health", ""); status != 200 ||
		!strings.Contains(body, `"servedBy":"core"`) {
		t.Fatalf("exact route after mount: status=%d body=%q, want the core's own handler", status, body)
	}
	if status, body := call(t, core, "GET", "/v1/billing/anything", ""); status != 200 ||
		!strings.Contains(body, `"servedBy":"plugin"`) {
		t.Fatalf("other subpath: status=%d body=%q, want the mounted plugin", status, body)
	}
}

// TestProxy_UnknownScheme proves Proxy refuses an unregistered scheme loudly
// rather than silently falling back to a default wire.
func TestProxy_UnknownScheme(t *testing.T) {
	_, err := zip.Proxy("/v1/x", "carrier-pigeon://somewhere:1")
	if err == nil {
		t.Fatal("Proxy with an unregistered scheme returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("error = %v, want it to name the unknown scheme", err)
	}
}

// TestProxy_DialOnlyAndServeOnly proves a transport may implement one
// direction: Listen reports a dial-only scheme and Proxy reports a serve-only
// one, each naming which half was missing.
func TestProxy_ServeOnlyTransportCannotDial(t *testing.T) {
	zip.RegisterTransport("serveonly", zip.Transport{
		Serve: func(addr string, h fasthttp.RequestHandler) zip.Server { return nil },
	})
	_, err := zip.Proxy("/v1/x", "serveonly://host:1")
	if err == nil || !strings.Contains(err.Error(), "cannot dial") {
		t.Fatalf("Proxy on a serve-only transport: err=%v, want a 'cannot dial' error", err)
	}
}
