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
