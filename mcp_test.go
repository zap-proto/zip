package zip_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	"github.com/zap-proto/zip"
)

type greetIn struct {
	Name string `json:"name" validate:"required"`
}
type greetOut struct {
	Message string `json:"message"`
}

// TestMCP_FreeToolSurface proves MCP is a FREE third projection of the typed-op
// registry: a Get/Post[In,Out] handler shows up as an MCP tool (tools/list) with
// the same JSON Schema OpenAPI uses, and tools/call runs the exact same fn and
// returns its output — no per-tool wiring. Served over the app's transports.
func TestMCP_FreeToolSurface(t *testing.T) {
	app := zip.New(zip.Config{AppName: "greeter", DisableStartupMessage: true})
	zip.Post(app, "/v1/greet", func(_ context.Context, in *greetIn) (*greetOut, error) {
		return &greetOut{Message: "hello " + in.Name}, nil
	}, zip.WithOperationID("greet"), zip.WithSummary("Greet someone by name"))

	const httpAddr = "127.0.0.1:18099"
	go func() { _ = app.Listen("http://" + httpAddr) }()
	defer func() { _ = app.Shutdown() }()
	waitHTTP(t, "http://"+httpAddr+"/.well-known/openapi.json")

	base := "http://" + httpAddr + "/mcp"

	// initialize
	init := rpc(t, base, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if init["result"].(map[string]any)["protocolVersion"] == nil {
		t.Fatalf("initialize missing protocolVersion: %v", init)
	}

	// tools/list — the typed op must appear as a tool with a real inputSchema.
	list := rpc(t, base, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("want 1 tool, got %d: %v", len(tools), tools)
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "greet" || tool["description"] != "Greet someone by name" {
		t.Fatalf("tool metadata wrong: %v", tool)
	}
	schema := tool["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if props["name"] == nil {
		t.Fatalf("inputSchema missing 'name' property (schemaOf projection): %v", schema)
	}

	// tools/call — runs the SAME fn and returns its output as text content.
	call := rpc(t, base, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"greet","arguments":{"name":"ada"}}}`)
	content := call["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"message":"hello ada"`) {
		t.Fatalf("tools/call output wrong: %q", text)
	}

	// validation flows through: a missing required arg is an isError result.
	bad := rpc(t, base, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"greet","arguments":{}}}`)
	if bad["result"].(map[string]any)["isError"] != true {
		t.Fatalf("missing required 'name' should be isError: %v", bad)
	}
}

func rpc(t *testing.T, url, body string) map[string]any {
	t.Helper()
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(url)
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetBodyString(body)
	if err := fasthttp.Do(req, resp); err != nil {
		t.Fatalf("mcp rpc %q: %v", body, err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		t.Fatalf("mcp rpc decode: %v (body=%s)", err, resp.Body())
	}
	return out
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		code, _, err := fasthttp.Get(nil, url)
		if err == nil && code == 200 {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("%s never became reachable", url)
}
