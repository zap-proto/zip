package zip

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	zapmcp "github.com/zap-proto/mcp"
)

// [MCPConfig.Source] and [MCPConfig.Addr] are each complete on their own and did
// not compose: a door served on a socket answers frames, and a frame carries no
// gateway assertion, so [App.list] returned early on a caller it could not name
// and served only the build-time bytes. The per-caller half — the whole reason a
// Source exists — was absent on exactly the transport that has no HTTP request.
//
// It presented as success. The door bound, logged `per-caller: true`, answered
// initialize, tools/list and tools/call, and probed healthy. What it dropped was
// invisible from the door's own side: an agent simply never saw a tenant's tools
// and nothing said why. These pin both directions of the fix.

// identSource is a Source whose whole answer is the caller, which is what makes
// it able to tell a door that states one from a door that does not.
type identSource struct{}

func (identSource) Tools(ctx context.Context) []map[string]any {
	org := CallerOf(ctx).Org
	if org == "" {
		return nil
	}
	return []map[string]any{{
		"name": org + "_own", "description": "a tool only " + org + " has",
		"inputSchema": map[string]any{"type": "object"},
	}}
}

func (identSource) Call(ctx context.Context, name string, _ json.RawMessage) (any, error) {
	if org := CallerOf(ctx).Org; org != "" && name == org+"_own" {
		return map[string]any{"ran": name, "org": org}, nil
	}
	return nil, Errorf(404, "unknown tool: %s", name)
}

// identApp is one typed op plus a per-caller half, which is the shape every
// assertion below is about.
func identApp(t *testing.T, cfg MCPConfig) *App {
	t.Helper()
	cfg.Source = identSource{}
	app := New(Config{AppName: "ident", DisableStartupMessage: true, MCP: cfg})
	Get(app, "/v1/ping", func(context.Context, *struct{}) (*struct {
		Pong bool `json:"pong"`
	}, error) {
		return &struct {
			Pong bool `json:"pong"`
		}{Pong: true}, nil
	}, WithOperationID("ping"))
	return app
}

// A declaration this process cannot honour is refused before anything binds,
// because the alternative is a door that serves half its tools while every
// signal it emits reads healthy.
func TestMCP_ZapDoorWithAPerCallerHalfRefusesWithoutIdentify(t *testing.T) {
	app := identApp(t, MCPConfig{Addr: filepath.Join(sockDir(t), "m.sock")})
	err := app.Listen(Addr(filepath.Join(sockDir(t), "a.sock")))
	if err == nil {
		t.Fatal("Listen accepted a ZAP door with a Source and no Identify — that door serves the build-time bytes to every caller and says nothing about it")
	}
	// The message has to name both ways out, or it sends the reader to the
	// framework rather than to their own declaration.
	for _, want := range []string{"MCP.Addr", "MCP.Identify", "anonymous"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not mention %q: %v", want, err)
		}
	}
	// Nothing came up. A refusal that leaves a listener bound is a worse outcome
	// than the defect it refuses.
	if n := app.listening.Load(); n != 0 {
		t.Fatalf("listening = %d after a refusal; want 0", n)
	}
}

// A door with no per-caller half is untouched: Addr alone is exactly the
// typed-op projection and needs nobody's identity to serve it.
func TestMCP_ZapDoorWithoutASourceNeedsNoIdentify(t *testing.T) {
	app := quiet("plain")
	app.cfg.MCP.Addr = filepath.Join(sockDir(t), "m.sock")
	Get(app, "/v1/ping", func(context.Context, *struct{}) (*struct{}, error) {
		return &struct{}{}, nil
	}, WithOperationID("ping"))
	if err := app.checkMCP(); err != nil {
		t.Fatalf("a door with nothing per-caller was refused: %v", err)
	}
}

// With an identity the two halves are one door: the tenant's own tool lists and
// runs over ZAP, on a socket, with no HTTP request anywhere in the path.
func TestMCP_IdentifyPutsTheCallerOnTheZapDoor(t *testing.T) {
	app := identApp(t, MCPConfig{Identify: func(ctx context.Context) Caller {
		// A real deployment reads zapmcp.Conn(ctx) — the peer credential, the
		// socket, its key material. What matters here is that the answer comes
		// from the CONNECTION and never from the frame, which the caller wrote.
		if zapmcp.Conn(ctx) == nil {
			return Caller{}
		}
		return Caller{Org: "acme", User: "z"}
	}})

	sock := filepath.Join(sockDir(t), "mcp.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &zapmcp.Server{Handler: app.zapDoor()}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })

	c := zapmcp.Dial("unix", sock)
	t.Cleanup(c.CloseIdleConnections)
	ctx := context.Background()

	// This app never served HTTP, so nothing here could have read a header.
	if app.prepared.Load() {
		t.Fatal("the router was built — this door was supposed to be ZAP only")
	}

	out, err := c.Call(ctx, "tools/list", nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("tools/list result: %v (%s)", err, out)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "acme_own" || names[1] != "ping" {
		t.Fatalf("tools = %v, want [acme_own ping] — the per-caller half is missing from the ZAP door", names)
	}

	// And it is CALLABLE, which is the half a list alone would not prove.
	if out, err = c.Call(ctx, "tools/call",
		json.RawMessage(`{"name":"acme_own","arguments":{}}`)); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if text := contentText(t, out); text != `{"org":"acme","ran":"acme_own"}` {
		t.Fatalf("content = %q", text)
	}
}

// The identity is the DOOR's, not the caller's: a frame that names an org is not
// how a caller becomes one. This is the property that makes the seam safe to
// hand a socket at all.
func TestMCP_IdentifyIsNotSomethingTheFrameCanState(t *testing.T) {
	app := identApp(t, MCPConfig{Identify: func(context.Context) Caller {
		return Caller{Org: "acme"}
	}})
	door := app.zapDoor()

	// A frame carrying another tenant's name in its own params.
	ans := door(context.Background(), &zapmcp.Frame{
		Kind: zapmcp.Request, ID: "1", Method: "tools/call",
		Params: json.RawMessage(`{"name":"evil_own","arguments":{"org":"evil"}}`),
	})
	b, err := json.Marshal(ans)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "unknown tool") {
		t.Fatalf("a frame named its own org and was served: %s", b)
	}
}
