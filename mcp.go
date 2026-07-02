package zip

import (
	"encoding/json"

	"github.com/gofiber/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// MCP — the THIRD projection. The same typed-op registry (a.ops) that produces
// the REST routes and the OpenAPI doc also produces a Model Context Protocol
// tool surface, for FREE: every typed handler (Get/Post[In,Out]) becomes an MCP
// tool whose inputSchema is schemaOf(In) and whose call runs the exact same fn
// (op.invoke). One value (the op), three projections (REST · OpenAPI · MCP).
//
// Because /mcp is an ordinary route on the app, it is served over EVERY
// transport the app Listens on — so ZAP-native MCP is automatic: an agent
// speaking ZAP gets the tool surface with zero extra wiring. Enabled by default.

// MCPConfig configures the auto-derived MCP surface.
type MCPConfig struct {
	// Disabled suppresses the /mcp route (MCP is on by default — it's free).
	Disabled bool
	// Path overrides the mount path (default "/mcp").
	Path string
	// Name is the server name reported to MCP clients (default AppName, else "zip").
	Name string
}

// mcpProtocolVersion is the MCP spec revision zip implements.
const mcpProtocolVersion = "2025-06-18"

func (a *App) mcpPath() string {
	if a.cfg.MCP.Path != "" {
		return a.cfg.MCP.Path
	}
	return "/mcp"
}

func (a *App) mcpName() string {
	switch {
	case a.cfg.MCP.Name != "":
		return a.cfg.MCP.Name
	case a.cfg.AppName != "":
		return a.cfg.AppName
	default:
		return "zip"
	}
}

// installMCP mounts the JSON-RPC 2.0 MCP endpoint when there are typed ops to
// expose. Called from prepare() alongside installOpenAPIRoutes.
func (a *App) installMCP() {
	if a.cfg.MCP.Disabled || len(a.ops) == 0 {
		return
	}
	a.fiber.Post(a.mcpPath(), a.handleMCP)
	a.logger.Info("zip mcp", "path", a.mcpPath(), "tools", len(a.ops))
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// handleMCP dispatches one JSON-RPC 2.0 MCP message.
func (a *App) handleMCP(fc fiber.Ctx) error {
	var req mcpRequest
	if err := json.Unmarshal(fc.Body(), &req); err != nil {
		return fc.JSON(mcpErr(nil, -32700, "parse error"))
	}
	switch req.Method {
	case "initialize":
		return fc.JSON(mcpResult(req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": a.mcpName(), "version": a.cfg.OpenAPI.Version},
		}))
	case "tools/list":
		return fc.JSON(mcpResult(req.ID, map[string]any{"tools": a.mcpTools()}))
	case "tools/call":
		return a.mcpCall(fc, req)
	case "ping":
		return fc.JSON(mcpResult(req.ID, map[string]any{}))
	default:
		// notifications/* carry no id and expect no result — ack with 202.
		if len(req.ID) == 0 {
			return fc.SendStatus(fiber.StatusAccepted)
		}
		return fc.JSON(mcpErr(req.ID, -32601, "method not found: "+req.Method))
	}
}

// mcpTools projects every typed op into an MCP tool descriptor. The inputSchema
// is the SAME schemaOf(InType) the OpenAPI doc uses — one schema, two surfaces.
func (a *App) mcpTools() []map[string]any {
	tools := make([]map[string]any, 0, len(a.ops))
	for _, op := range a.ops {
		tools = append(tools, map[string]any{
			"name":        opName(op),
			"description": op.Summary,
			"inputSchema": schemaOf(op.InType, map[string]any{}),
		})
	}
	return tools
}

// mcpCall runs a tools/call: find the op by name, invoke the SAME handler core
// the REST route uses, and return its JSON result as MCP text content. A handler
// error is reported as MCP isError content (not a JSON-RPC transport error), per
// the MCP spec — the model sees the failure and can react.
func (a *App) mcpCall(fc fiber.Ctx, req mcpRequest) error {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &params)

	op := a.opByName(params.Name)
	if op == nil || op.invoke == nil {
		return fc.JSON(mcpErr(req.ID, -32602, "unknown tool: "+params.Name))
	}

	out, err := op.invoke(fc.Context(), params.Arguments)
	if err != nil {
		return fc.JSON(mcpResult(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}))
	}
	text := "null"
	if out != nil {
		if b, mErr := jsonenc.Marshal(out); mErr == nil {
			text = string(b)
		}
	}
	return fc.JSON(mcpResult(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}))
}

func (a *App) opByName(name string) *registeredOp {
	for _, op := range a.ops {
		if opName(op) == name {
			return op
		}
	}
	return nil
}

// opName is the stable tool/operation id: the explicit OperationID, else the
// method+path default (shared with OpenAPI so the two surfaces agree).
func opName(op *registeredOp) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	return defaultOpID(op.Method, op.Path)
}

func mcpResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": idOrNull(id), "result": result}
}

func mcpErr(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": idOrNull(id), "error": map[string]any{"code": code, "message": msg}}
}

func idOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
