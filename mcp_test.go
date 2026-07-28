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

	httpAddr := freeAddr(t)
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

type stockIn struct {
	SKU   string `json:"sku" validate:"required"`
	Depot string `json:"depot"`
}
type stockOut struct {
	Units int `json:"units"`
}

// TestMCP_ToolsCarryZipdocProse proves the tool list reads the SAME cmd/zipdoc
// extraction the OpenAPI doc and the CLI help read. Before this, mcpTools took
// only WithSummary, so an op documented the canonical way — a doc comment, no
// WithSummary — projected into a tool with an empty description and a schema
// whose fields said nothing, which is the one thing a model needs to choose it.
func TestMCP_ToolsCarryZipdocProse(t *testing.T) {
	// What cmd/zipdoc emits into an init() for a documented handler.
	zip.Describe("POST /v1/stock", zip.Doc{
		Description: "stock reports on-hand units for a SKU. It reads the depot ledger, not the\ncatalog: the catalog says what may be sold and only the ledger says what is\nthere.",
		Fields: map[string]string{
			"stockIn.sku":   "SKU is the catalog identifier, not the supplier's part number.",
			"stockIn.depot": "Depot narrows the count to one warehouse; empty counts them all.",
		},
	})

	app := zip.New(zip.Config{AppName: "wms", DisableStartupMessage: true})
	// No WithSummary — the doc comment IS the description.
	zip.Post(app, "/v1/stock", func(_ context.Context, _ *stockIn) (*stockOut, error) {
		return &stockOut{Units: 3}, nil
	}, zip.WithOperationID("stock"))

	httpAddr := freeAddr(t)
	go func() { _ = app.Listen("http://" + httpAddr) }()
	defer func() { _ = app.Shutdown() }()
	waitHTTP(t, "http://"+httpAddr+"/.well-known/openapi.json")

	list := rpc(t, "http://"+httpAddr+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("want 1 tool, got %d: %v", len(tools), tools)
	}
	tool := tools[0].(map[string]any)

	if !strings.Contains(tool["description"].(string), "reads the depot ledger") {
		t.Fatalf("tool description did not come from the zipdoc extraction: %q", tool["description"])
	}

	// Field prose reaches the schema too — same "Type.field" key the CLI reads.
	props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	sku, _ := props["sku"].(map[string]any)
	if sku == nil || !strings.Contains(sku["description"].(string), "supplier's part number") {
		t.Fatalf("inputSchema field lost its doc comment: %v", props)
	}

	// The OpenAPI projection of the same op agrees — one comment, two surfaces.
	spec := getJSON(t, "http://"+httpAddr+"/.well-known/openapi.json")
	post := spec["paths"].(map[string]any)["/v1/stock"].(map[string]any)["post"].(map[string]any)
	if post["description"] != tool["description"] {
		t.Fatalf("spec and tool describe the same op differently:\n spec=%q\n tool=%q", post["description"], tool["description"])
	}
}

// TestMCP_SummaryStillDescribesUndocumentedOp keeps the fallback honest: an op
// whose package cmd/zipdoc never ran over still names itself from WithSummary.
func TestMCP_SummaryStillDescribesUndocumentedOp(t *testing.T) {
	app := zip.New(zip.Config{AppName: "bare", DisableStartupMessage: true})
	zip.Post(app, "/v1/ping", func(_ context.Context, _ *greetIn) (*greetOut, error) {
		return &greetOut{Message: "pong"}, nil
	}, zip.WithOperationID("ping"), zip.WithSummary("Ping the service"))

	httpAddr := freeAddr(t)
	go func() { _ = app.Listen("http://" + httpAddr) }()
	defer func() { _ = app.Shutdown() }()
	waitHTTP(t, "http://"+httpAddr+"/.well-known/openapi.json")

	list := rpc(t, "http://"+httpAddr+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tool := list["result"].(map[string]any)["tools"].([]any)[0].(map[string]any)
	if tool["description"] != "Ping the service" {
		t.Fatalf("WithSummary fallback lost: %v", tool)
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	code, body, err := fasthttp.Get(nil, url)
	if err != nil || code != 200 {
		t.Fatalf("get %s: code=%d err=%v", url, code, err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return out
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
