package zip_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// buildPluginBin compiles internal/testplugin to a FILE and returns the path,
// because the catalogue is captured by RUNNING the binary — the build-time step,
// which is exactly where a host's Plugin.Tools comes from.
func buildPluginBin(t testing.TB, version string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "testplugin-"+version)
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", out, "./internal/testplugin")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build test plugin (no toolchain?): %v\n%s", err, b)
	}
	return out
}

// catalogue runs `<bin> tools` — the plugin projecting its own MCP surface.
func catalogue(t testing.TB, bin string) []byte {
	t.Helper()
	b, err := exec.Command(bin, "tools").Output()
	if err != nil {
		t.Fatalf("%s tools: %v", bin, err)
	}
	return b
}

func toolNames(t *testing.T, body string) []string {
	t.Helper()
	var env struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("tools/list is not JSON-RPC: %v\nbody=%s", err, body)
	}
	names := make([]string, 0, len(env.Result.Tools))
	for _, tl := range env.Result.Tools {
		if tl.Description == "" {
			t.Errorf("tool %q has an EMPTY description — a model paying context for a nameless tool is a silent failure", tl.Name)
		}
		if len(tl.InputSchema) == 0 {
			t.Errorf("tool %q has no inputSchema", tl.Name)
		}
		names = append(names, tl.Name)
	}
	return names
}

func running(t *testing.T, app *zip.App) []string {
	t.Helper()
	var up []string
	for _, s := range app.Plugins() {
		if s.Running {
			up = append(up, s.Name)
		}
	}
	return up
}

// TestMCP_ComposedListWakesNothing is the load-bearing proof of the whole
// feature: a host composing LAZY plugins answers tools/list from their build-time
// catalogues with ZERO child processes started, and a tools/call starts exactly
// the ONE plugin that owns the named tool.
//
// A host that woke every child to answer tools/list would destroy the single
// invariant that makes a large lazy fleet affordable, and tools/list is the
// method an MCP client calls constantly. So the process count is asserted, not
// argued.
func TestMCP_ComposedListWakesNothing(t *testing.T) {
	bin := buildPluginBin(t, "v1")
	tools := catalogue(t, bin)
	if !strings.Contains(string(tools), "get_v1_demo_version") {
		t.Fatalf("plugin catalogue does not name its own op: %s", tools)
	}

	// A host with NO typed ops of its own — the hanzoai/cloud shape — serving the
	// door at a versioned public path.
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true,
		MCP: zip.MCPConfig{Path: "/v1/mcp"}})
	for _, name := range []string{"demo", "other"} {
		p := zip.Plugin{Name: name, Path: bin, Lazy: true}
		if name == "demo" {
			p.Tools = tools // only one of them declares a catalogue
		}
		if err := app.Add(zip.Load(p, "/v1/"+name)); err != nil {
			t.Fatalf("Add(Load %s): %v", name, err)
		}
	}
	app.Prepare()

	if up := running(t, app); len(up) != 0 {
		t.Fatalf("a lazy Load started something: %v", up)
	}

	// tools/list, three times over — an MCP client calls it constantly.
	var names []string
	for i := 0; i < 3; i++ {
		status, body := call(t, app, "POST", "/v1/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if status != 200 {
			t.Fatalf("tools/list: status %d body=%s", status, body)
		}
		names = toolNames(t, body)
	}
	if up := running(t, app); len(up) != 0 {
		t.Fatalf("tools/list WOKE %v — the list must cost zero processes", up)
	}
	if len(names) == 0 {
		t.Fatal("tools/list is empty: the host has no ops of its own, so the plugin catalogue is the whole surface")
	}
	var found bool
	for _, n := range names {
		if n == "get_v1_demo_version" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the plugin's op is not on the host's door: %v", names)
	}
	// Sorted, because the same projection is committed as an artifact.
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("tools/list is not sorted by name: %v", names)
		}
	}

	// A name nobody owns never reaches a process either.
	_, body := call(t, app, "POST", "/v1/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	if !strings.Contains(body, "unknown tool") {
		t.Fatalf("unknown tool should be -32602: %s", body)
	}
	if up := running(t, app); len(up) != 0 {
		t.Fatalf("an unknown tools/call woke %v", up)
	}

	// tools/call — the ONE trigger, and it wakes ONE child.
	status, body := call(t, app, "POST", "/v1/mcp",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_v1_demo_version","arguments":{}}}`)
	if status != 200 {
		t.Fatalf("tools/call: status %d body=%s", status, body)
	}
	if !strings.Contains(body, `\"version\":\"v1\"`) {
		t.Fatalf("tools/call did not reach the plugin's own handler: %s", body)
	}
	up := running(t, app)
	if len(up) != 1 || up[0] != "demo" {
		t.Fatalf("tools/call started %v — exactly one owner, and only the owner", up)
	}
	_ = app.Shutdown()
}

// TestMCP_DuplicateToolNameRefusedAtLoad pins the Load-time analogue of a
// duplicate prefix claim: a tool NAME is dispatch, so two owners make it
// unroutable and the composition must fail with both names rather than silently
// forward every call to whichever loaded first.
func TestMCP_DuplicateToolNameRefusedAtLoad(t *testing.T) {
	bin := buildPluginBin(t, "v1")
	tools := catalogue(t, bin)

	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	if err := app.Add(zip.Load(zip.Plugin{Name: "a", Path: bin, Lazy: true, Tools: tools}, "/v1/a")); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	err := app.Add(zip.Load(zip.Plugin{Name: "b", Path: bin, Lazy: true, Tools: tools}, "/v1/b"))
	if err == nil {
		t.Fatal("two plugins claiming one tool name must be refused at Load")
	}
	if !strings.Contains(err.Error(), "get_v1_demo_") || !strings.Contains(err.Error(), `already served by plugin "a"`) {
		t.Fatalf("the refusal must name the tool and the holder: %v", err)
	}
	// The refused Load left nothing behind: its prefix is free for a real owner.
	if err := app.Add(zip.Load(zip.Plugin{Name: "b2", Path: bin, Lazy: true}, "/v1/b")); err != nil {
		t.Fatalf("a refused Load must release its prefixes: %v", err)
	}
}

// TestMCP_MalformedCatalogueRefusedAtLoad — a catalogue that is not a tool array
// is a build bug, and it fails at Load rather than at the first tools/list.
func TestMCP_MalformedCatalogueRefusedAtLoad(t *testing.T) {
	bin := buildPluginBin(t, "v1")
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	err := app.Add(zip.Load(zip.Plugin{Name: "a", Path: bin, Lazy: true, Tools: []byte(`{"not":"an array"}`)}, "/v1/a"))
	if err == nil || !strings.Contains(err.Error(), "not a JSON array") {
		t.Fatalf("a malformed catalogue must be refused at Load: %v", err)
	}
}

// TestMCP_NoOpsNoCatalogueNoDoor — a host with neither is not an MCP server, and
// must not answer the door with an empty tool list a client would cache.
func TestMCP_NoOpsNoCatalogueNoDoor(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true,
		MCP: zip.MCPConfig{Path: "/v1/mcp"}})
	app.Prepare()
	status, _ := call(t, app, "POST", "/v1/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if status != 404 {
		t.Fatalf("no ops and no catalogue must serve no door, got %d", status)
	}
}
