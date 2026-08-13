package zip_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The HOST side of the per-caller half: a plugin whose catalogue is INCOMPLETE by
// construction. The build-time array still answers tools/list for free; a request
// that NAMES a caller also asks the open plugin, and only then.

// asOrg posts a JSON-RPC message to the door on behalf of org ("" = anonymous).
func asOrg(t *testing.T, app *zip.App, org, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", "/v1/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if org != "" {
		req.Header.Set(zip.HeaderOrg, org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// openHost is a host with one lazy plugin declared Open, the hanzoai/cloud shape.
func openHost(t *testing.T) *zip.App {
	t.Helper()
	bin := buildPluginBin(t, "v1")
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true,
		MCP: zip.MCPConfig{Path: "/v1/mcp"}})
	app.Use(must(zip.Load(zip.Plugin{
		Name: "demo", Path: bin, Lazy: true, Open: true, Tools: catalogue(t, bin),
	}, "/v1/demo")))
	t.Cleanup(func() { _ = app.Shutdown() })
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

// TestOpen_AnonymousListWakesNothing: the invariant that makes the door
// affordable survives. A per-caller answer needs a caller, so a list that names
// nobody is still the build-time array and still starts no process.
func TestOpen_AnonymousListWakesNothing(t *testing.T) {
	app := openHost(t)
	status, body := asOrg(t, app, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if status != 200 {
		t.Fatalf("tools/list: %d %s", status, body)
	}
	names := toolNames(t, body)
	if len(names) == 0 {
		t.Fatal("the plugin's build-time catalogue is the whole surface here and it is empty")
	}
	for _, n := range names {
		if strings.HasSuffix(n, "_own") {
			t.Fatalf("an anonymous list carried a tenant's tool: %v", names)
		}
	}
	if up := running(t, app); len(up) != 0 {
		t.Fatalf("an anonymous tools/list WOKE %v — it must cost zero processes", up)
	}
}

// TestOpen_NamedCallerGetsItsOwnTools: with a caller named, the host asks the open
// plugin and splices its answer in beside the build-time half.
func TestOpen_NamedCallerGetsItsOwnTools(t *testing.T) {
	app := openHost(t)
	status, body := asOrg(t, app, "acme", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if status != 200 {
		t.Fatalf("tools/list: %d %s", status, body)
	}
	names := toolNames(t, body)
	var mine, projected bool
	for _, n := range names {
		switch n {
		case "acme_own":
			mine = true
		case "get_demo_version":
			projected = true
		}
	}
	if !projected {
		t.Fatalf("the build-time half is gone: %v", names)
	}
	if !mine {
		t.Fatalf("the caller's own tool is not on the door: %v", names)
	}

	// A DIFFERENT caller gets a different list off the same door.
	_, body = asOrg(t, app, "rival", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	for _, n := range toolNames(t, body) {
		if n == "acme_own" {
			t.Fatalf("one tenant's tool reached another's list: %v", toolNames(t, body))
		}
	}
}

// TestOpen_CallReachesTheOpenPlugin: a name NO catalogue claims is not -32602 when
// there is an open plugin — it goes there, and the plugin's own Source answers.
func TestOpen_CallReachesTheOpenPlugin(t *testing.T) {
	app := openHost(t)
	_, body := asOrg(t, app, "acme",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"acme_own","arguments":{}}}`)
	if !strings.Contains(body, `\"org\":\"acme\"`) {
		t.Fatalf("the open plugin did not run the caller's tool: %s", body)
	}
	if strings.Contains(body, `"isError":true`) {
		t.Fatalf("the call was refused: %s", body)
	}

	// And a name nobody has anywhere still fails, as itself.
	_, body = asOrg(t, app, "acme",
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nobody_has_this","arguments":{}}}`)
	if !strings.Contains(body, "unknown tool") {
		t.Fatalf("an unknown tool must say so: %s", body)
	}
}

// TestOpen_OnlyOneMayBeOpen: an unclaimed tool name has to resolve somewhere, and
// two candidates would make it ambiguous — the same reason two plugins may not own
// one tool name. The second Load is refused, naming the first.
func TestOpen_OnlyOneMayBeOpen(t *testing.T) {
	bin := buildPluginBin(t, "v1")
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true,
		MCP: zip.MCPConfig{Path: "/v1/mcp"}})
	t.Cleanup(func() { _ = app.Shutdown() })
	app.Use(must(zip.Load(zip.Plugin{Name: "first", Path: bin, Lazy: true, Open: true}, "/v1/first")))
	app.Use(must(zip.Load(zip.Plugin{Name: "second", Path: bin, Lazy: true, Open: true}, "/v1/second")))
	err := app.Build()
	if err == nil {
		t.Fatal("a second open plugin must be refused")
	}
	if !strings.Contains(err.Error(), "first") {
		t.Fatalf("the refusal must name the plugin already holding it: %v", err)
	}
}
