package zip

import (
	"context"
	"encoding/json"
	"testing"

	zapmcp "github.com/zap-proto/mcp"
)

// 2026-07-28 removed the mandatory handshake, so a client may inspect a server
// with server/discover instead of initialize. Answering one and not the other
// reads as a pre-2026 door however stateless the transport already is.
func TestDiscoverAnswersLikeInitialize(t *testing.T) {
	a := New(Config{})
	get := func(method string) map[string]any {
		f := a.MCP(context.Background(), &zapmcp.Frame{Method: method, Kind: zapmcp.Request, ID: "1"})
		if f == nil {
			t.Fatalf("%s: nil frame", method)
		}
		b, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("%s: marshal: %v", method, err)
		}
		var env struct {
			Result map[string]any `json:"result"`
			Error  any            `json:"error"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("%s: unmarshal %s: %v", method, b, err)
		}
		if env.Error != nil {
			t.Fatalf("%s: error %v", method, env.Error)
		}
		return env.Result
	}
	in, disc := get("initialize"), get("server/discover")
	if in["protocolVersion"] != "2026-07-28" {
		t.Fatalf("protocolVersion = %v, want 2026-07-28", in["protocolVersion"])
	}
	if disc["protocolVersion"] != in["protocolVersion"] {
		t.Fatalf("server/discover %v != initialize %v", disc["protocolVersion"], in["protocolVersion"])
	}
}

// A client that asks for a revision this door speaks is answered in it; one that
// asks for anything else is answered in the newest. The official TypeScript and
// Python clients both refuse an initialize answered in a revision they did not
// ask for and do not know.
func TestInitializeAnswersTheRevisionTheClientAsked(t *testing.T) {
	a := New(Config{})
	for asked, want := range map[string]string{
		"2025-11-25": "2025-11-25",
		"2025-06-18": "2025-06-18",
		"2026-07-28": "2026-07-28",
		"2024-11-05": mcpProtocolVersion,
		"":           mcpProtocolVersion,
	} {
		params, err := json.Marshal(map[string]any{"protocolVersion": asked})
		if err != nil {
			t.Fatal(err)
		}
		f := a.MCP(context.Background(), &zapmcp.Frame{Method: "initialize", Kind: zapmcp.Request, ID: "1", Params: params})
		var got struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(f.Result, &got); err != nil {
			t.Fatalf("asked %q: %v", asked, err)
		}
		if got.ProtocolVersion != want {
			t.Errorf("asked %q: answered %q, want %q", asked, got.ProtocolVersion, want)
		}
	}
}
