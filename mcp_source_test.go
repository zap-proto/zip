package zip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	fiber "github.com/zap-proto/fiber/v3"
)

// The PER-CALLER half of the door. A typed op is a value known at build time and
// is served as bytes; a tenant's own capability is a row and cannot be. These pin
// that both reach ONE tools/list, that the build-time half is untouched when
// nobody is asking, and that a projected name can never be shadowed by a tenant's.

// orgSource is a Source whose answer depends on the caller, which is the whole
// point of the seam: it reads the org off the context the request is served on.
type orgSource struct {
	// calls is atomic because TestConcurrentListsDoNotRace drives this fixture
	// from 64 goroutines: a plain counter here would report a race in the TEST and
	// hide whether the library has one.
	calls atomic.Int64
	args  json.RawMessage
}

func (s *orgSource) Tools(ctx context.Context) []map[string]any {
	s.calls.Add(1)
	org, _ := ctx.Value(orgKey{}).(string)
	if org == "" {
		return nil
	}
	return []map[string]any{
		{"name": org + "_search", "description": "search " + org, "inputSchema": map[string]any{"type": "object"}},
		// A name the typed-op projection already holds: it must not appear twice.
		{"name": "get_ping", "description": "shadow attempt", "inputSchema": map[string]any{"type": "object"}},
	}
}

func (s *orgSource) Call(ctx context.Context, name string, args json.RawMessage) (any, error) {
	s.args = args
	org, _ := ctx.Value(orgKey{}).(string)
	if !strings.HasPrefix(name, org+"_") {
		return nil, Errorf(404, "unknown tool: %s", name)
	}
	return map[string]any{"ran": name, "org": org}, nil
}

type orgKey struct{}

// sourceApp mounts one typed op plus a Source, and parks the request's org on the
// context the way a host's identity middleware does.
func sourceApp(t *testing.T, src Source) *App {
	t.Helper()
	app := New(Config{AppName: "src", MCP: MCPConfig{Source: src}})
	app.Use(H(func(c *Ctx) error {
		c.SetContext(context.WithValue(c.Context(), orgKey{}, c.Org()))
		return c.Continue()
	}))
	Get(app, "/ping", ping)
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

type pingIn struct{}

type pingOut struct {
	OK bool `json:"ok"`
}

func ping(_ context.Context, _ *pingIn) (*pingOut, error) { return &pingOut{OK: true}, nil }

// TestSourceToolsJoinTheList: a caller's own tools are on the SAME tools/list as
// the typed ops — one door, two halves.
func TestSourceToolsJoinTheList(t *testing.T) {
	src := &orgSource{}
	app := sourceApp(t, src)

	names := toolNamesFor(t, app, "acme")
	if !names["get_ping"] {
		t.Fatalf("the projected op is missing: %v", keys(names))
	}
	if !names["acme_search"] {
		t.Fatalf("the caller's own tool is missing: %v", keys(names))
	}
	if n := src.calls.Load(); n != 1 {
		t.Fatalf("Source.Tools called %d times, want 1", n)
	}
}

// TestSourceNeverShadowsAProjectedOp: the build-time half wins a name collision,
// and the name appears exactly once. A duplicate name in a tools/list is
// unroutable, and the projected op is the one the whole fleet agreed on.
func TestSourceNeverShadowsAProjectedOp(t *testing.T) {
	app := sourceApp(t, &orgSource{})
	body := listBody(t, app, "acme")
	if n := strings.Count(body, `"get_ping"`); n != 1 {
		t.Fatalf("get_ping appears %d times, want 1: %s", n, body)
	}
	if strings.Contains(body, "shadow attempt") {
		t.Fatalf("the tenant's description shadowed the projected op: %s", body)
	}
}

// TestNoCallerNoAsk: a tools/list that names nobody is the memcpy it always was.
// A per-caller answer needs a caller, so an anonymous probe must not reach the
// Source at all.
func TestNoCallerNoAsk(t *testing.T) {
	src := &orgSource{}
	app := sourceApp(t, src)

	names := toolNamesFor(t, app, "")
	if !names["get_ping"] {
		t.Fatalf("the projected op must still be listed: %v", keys(names))
	}
	if len(names) != 1 {
		t.Fatalf("anonymous list must be the build-time half alone, got %v", keys(names))
	}
	if n := src.calls.Load(); n != 0 {
		t.Fatalf("Source was asked %d times with no caller, want 0", n)
	}
}

// TestSourceCallRunsAndIsScopedToTheCaller: a name no projection claims goes to
// the Source, which resolves its caller off the context and not off the
// arguments — so one org's call can never name another org's tool.
func TestSourceCallRunsAndIsScopedToTheCaller(t *testing.T) {
	src := &orgSource{}
	app := sourceApp(t, src)

	res := callTool(t, app, "acme", "acme_search", `{"q":"x"}`)
	if !strings.Contains(res, `\"ran\":\"acme_search\"`) || !strings.Contains(res, `\"org\":\"acme\"`) {
		t.Fatalf("call did not run for the caller: %s", res)
	}
	if string(src.args) != `{"q":"x"}` {
		t.Fatalf("arguments not passed through verbatim: %s", src.args)
	}

	// Another org asking for acme's tool is refused by the Source, and the refusal
	// reaches the model as isError content rather than a transport failure.
	res = callTool(t, app, "other", "acme_search", `{}`)
	if !strings.Contains(res, `"isError":true`) {
		t.Fatalf("cross-tenant call must be refused: %s", res)
	}
}

// TestNoSourceIsTheOldDoor: with no Source and no open plugin, nothing in the
// per-caller path runs and the served bytes are exactly the rendered array.
func TestNoSourceIsTheOldDoor(t *testing.T) {
	app := New(Config{AppName: "plain"})
	Get(app, "/ping", ping)
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if app.hasCaller() {
		t.Fatal("a door with no Source and no open plugin has no per-caller half")
	}
	names := toolNamesFor(t, app, "acme")
	if len(names) != 1 || !names["get_ping"] {
		t.Fatalf("want exactly the projected op, got %v", keys(names))
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func listBody(t *testing.T, app *App, org string) string {
	t.Helper()
	return mcpPost(t, app, org, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
}

func callTool(t *testing.T, app *App, org, name, args string) string {
	t.Helper()
	return mcpPost(t, app, org, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
}

func mcpPost(t *testing.T, app *App, org, body string) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set(HeaderOrg, org)
	}
	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("mcp post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func toolNamesFor(t *testing.T, app *App, org string) map[string]bool {
	t.Helper()
	var env struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	body := listBody(t, app, org)
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("tools/list: %v (%s)", err, body)
	}
	out := map[string]bool{}
	for _, tl := range env.Result.Tools {
		out[tl.Name] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestCallerToolsDoNotLeakBetweenCallers is the regression for the sharpest bug
// this seam can have: the per-caller dedup writing into the FLEET's name set.
//
// It failed twice over, and silently both times. One tenant's tool name became
// "already claimed" for every tenant that asked afterwards, so the second org
// lost a capability it had — and could infer, from the absence, that someone else
// had a tool by that name. And two lists in flight at once were two writes to one
// map, which is a runtime FATAL that no recover() catches, on the one method an
// MCP client calls constantly.
func TestCallerToolsDoNotLeakBetweenCallers(t *testing.T) {
	app := sourceApp(t, &orgSource{})

	first := toolNamesFor(t, app, "acme")
	if !first["acme_search"] {
		t.Fatalf("acme's own tool is missing: %v", keys(first))
	}
	// The SAME org again, and then a different one: each must get its own tools,
	// every time. A name consumed by the first pass is a name the second loses.
	for i := 0; i < 3; i++ {
		if again := toolNamesFor(t, app, "acme"); !again["acme_search"] {
			t.Fatalf("pass %d lost the caller's own tool: %v", i, keys(again))
		}
	}
	if other := toolNamesFor(t, app, "rival"); !other["rival_search"] {
		t.Fatalf("a second tenant lost its tool to the first: %v", keys(other))
	}
	if other := toolNamesFor(t, app, "rival"); other["acme_search"] {
		t.Fatalf("one tenant's tool reached another's list: %v", keys(other))
	}
}

// TestConcurrentListsDoNotRace: run under -race, and fatal without the fix.
func TestConcurrentListsDoNotRace(t *testing.T) {
	app := sourceApp(t, &orgSource{})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			org := fmt.Sprintf("org%d", i%8)
			if names := toolNamesFor(t, app, org); !names[org+"_search"] {
				t.Errorf("concurrent list lost %s's own tool", org)
			}
		}(i)
	}
	wg.Wait()
}
