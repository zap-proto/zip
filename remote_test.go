package zip

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// remoteService starts a real zip app on a unix socket and returns its address
// and its own declaration — the two things a host needs, and the second of
// which it must be HANDED rather than go and ask for.
func remoteService(t *testing.T) (string, Declaration) {
	t.Helper()
	svc := quiet("ledger")
	Get(svc, "/v1/ledger/entries/:id", func(_ context.Context, in *invoiceIn) (*invoiceOut, error) {
		return &invoiceOut{ID: in.ID, Total: 7}, nil
	}, WithOperationID("getEntry"))
	svc.Get("/v1/ledger/raw", func(c *Ctx) error { return c.String(200, "raw ledger") })

	sock := filepath.Join(t.TempDir(), "ledger.sock")
	go func() { _ = svc.Listen(sock) }()
	t.Cleanup(func() { _ = svc.Shutdown() })

	// Wait for the SOCKET, not for App.listening. Listen increments that counter
	// as soon as it has constructed the servers and before the goroutines it
	// spawns have bound anything, so a reader that trusts it races the bind and
	// gets ECONNREFUSED from a mount that is perfectly correct. Dialling is the
	// only question whose answer means the service is reachable.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("unix", sock)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service never bound %s: %v", sock, err)
		}
		time.Sleep(time.Millisecond)
	}
	return sock, svc.Declaration()
}

// TestMount_DeclarationIsAnInputNotAFetch is the rule the whole design hangs
// on: building a program does no I/O. The address here goes nowhere, and the
// registry, the document and the tool list are still complete — because the
// declaration was handed in.
//
// If the walk dialled instead, this test could not exist: Registry() would be
// fallible and slow, and an OpenAPI document would depend on some other process
// being up at boot.
func TestMount_DeclarationIsAnInputNotAFetch(t *testing.T) {
	host := quiet("cloud")
	dead := "/nonexistent/nothing-listens-here.sock"
	err := host.Mount("/v1/ledger", dead, Declaration{
		Name: "ledger",
		Routes: []Route{
			{Method: "GET", Pattern: "/v1/ledger/entries/:id", Op: "getEntry"},
			{Method: "GET", Pattern: "/v1/ledger/raw"},
		},
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := build(host); err != nil {
		t.Fatalf("build: %v", err)
	}

	reg := host.Registry()
	if len(reg) != 1 {
		t.Fatalf("registry has %d ops, want 1 (the remote's declared op)", len(reg))
	}
	if reg[0].OperationID != "getEntry" || reg[0].Path != "/v1/ledger/entries/:id" {
		t.Errorf("remote op = %s %s, want getEntry /v1/ledger/entries/:id", reg[0].OperationID, reg[0].Path)
	}
	if reg[0].Origin != "ledger" {
		t.Errorf("remote op origin = %q, want ledger", reg[0].Origin)
	}
	// The document names it too — a mounted service is in the projections
	// because it is in the registry, not because the document learned to mount.
	spec := host.OpenAPISpec()
	if _, ok := spec["paths"].(map[string]map[string]any)["/v1/ledger/entries/{id}"]; !ok {
		t.Errorf("the document does not carry the mounted op")
	}
	// And nothing was dialled: an unreachable address built a complete program.
}

// TestMount_ServesAndCallsTheRemote: the leaf's routes proxy and its ops
// forward, over the same transport, to a service that really is somewhere else.
func TestMount_ServesAndCallsTheRemote(t *testing.T) {
	sock, decl := remoteService(t)
	if len(decl.Routes) != 2 {
		t.Fatalf("the service declared %d routes, want 2: %+v", len(decl.Routes), decl.Routes)
	}
	var typedOp string
	for _, r := range decl.Routes {
		if r.Pattern == "/v1/ledger/entries/:id" {
			typedOp = r.Op
		}
	}
	if typedOp != "getEntry" {
		t.Fatalf("the declaration does not name the op for its typed route: %+v", decl.Routes)
	}

	host := quiet("cloud")
	if err := host.Mount("/v1/ledger", sock, decl); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	host.Prepare()

	// 1. The proxied route reaches the remote's own handler.
	if code, body := wireGET(t, host, "/v1/ledger/raw"); code != 200 || body != "raw ledger" {
		t.Errorf("proxied route: %d %q", code, body)
	}
	if code, body := wireGET(t, host, "/v1/ledger/entries/e-9"); code != 200 || !strings.Contains(body, "e-9") {
		t.Errorf("proxied typed route: %d %q", code, body)
	}
	// 2. An address the remote did NOT declare falls through to the host.
	if code, _ := wireGET(t, host, "/v1/ledger/undeclared"); code != 404 {
		t.Errorf("undeclared remote path answered %d, want 404 — the mount is swallowing", code)
	}
	// 3. The op forwards BY NAME, so the tool an agent sees runs the remote's
	//    handler rather than finding a hole at the process boundary.
	out := rpcOn(t, host,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getEntry","arguments":{"id":"e-3"}}}`)
	if !strings.Contains(out, "e-3") {
		t.Errorf("tools/call on a mounted op did not reach the remote: %s", out)
	}
}

func rpcOn(t *testing.T, a *App, body string) string {
	t.Helper()
	req := postReq("/mcp", "application/json", []byte(body))
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return readAll(t, resp.Body)
}

func postReq(path, ctype string, body []byte) *http.Request {
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	return req
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}
