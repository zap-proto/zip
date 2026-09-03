package zip

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RustMCP is one generated Rust Model Context Protocol (MCP) server module.
type RustMCP struct {
	Crate  string
	Source []byte
}

// RustMCP renders a native Rust Model Context Protocol (MCP) tool surface and server dispatch.
// Every tool is self-documenting with rustdoc comments and JSON input schema.
func (a *App) RustMCP(crate string) (*RustMCP, error) {
	if crate == "" {
		crate = "mcp"
	}

	tools := a.MCPTools()
	var b strings.Builder
	b.WriteString("//! Code generated from the typed-op registry by zip. DO NOT EDIT.\n//!\n")
	fmt.Fprintf(&b, "//! Model Context Protocol (MCP) Server tools for %s.\n\n", crate)
	b.WriteString("use serde::{Deserialize, Serialize};\n")
	b.WriteString("use serde_json::Value;\n\n")

	b.WriteString("/// Tool descriptor exposed to MCP clients and agents.\n")
	b.WriteString("#[derive(Debug, Clone, Serialize, Deserialize)]\n")
	b.WriteString("pub struct ToolDescriptor {\n")
	b.WriteString("    pub name: &'static str,\n")
	b.WriteString("    pub description: &'static str,\n")
	b.WriteString("    pub input_schema: Value,\n")
	b.WriteString("}\n\n")

	b.WriteString("/// Catalogue of available MCP tools.\n")
	b.WriteString("pub static TOOLS: &[ToolDescriptor] = &[\n")

	// Sort tools by name
	sort.Slice(tools, func(i, j int) bool {
		return tools[i]["name"].(string) < tools[j]["name"].(string)
	})

	for _, tool := range tools {
		name := tool["name"].(string)
		desc := ""
		if d, ok := tool["description"].(string); ok {
			desc = d
		}
		schemaBytes, _ := json.Marshal(tool["inputSchema"])

		b.WriteString("    ToolDescriptor {\n")
		fmt.Fprintf(&b, "        name: %q,\n", name)
		fmt.Fprintf(&b, "        description: %q,\n", desc)
		fmt.Fprintf(&b, "        input_schema: serde_json::json!(%s),\n", string(schemaBytes))
		b.WriteString("    },\n")
	}
	b.WriteString("];\n\n")

	b.WriteString("/// Returns the list of available MCP tools for tools/list queries.\n")
	b.WriteString("pub fn list_tools() -> &'static [ToolDescriptor] {\n")
	b.WriteString("    TOOLS\n")
	b.WriteString("}\n\n")

	b.WriteString("/// Trait implemented by service handlers to process MCP tool calls.\n")
	b.WriteString("#[async_trait::async_trait]\n")
	b.WriteString("pub trait McpHandler: Send + Sync {\n")
	for _, tool := range tools {
		name := tool["name"].(string)
		methodName := snakeCase(name)
		desc := ""
		if d, ok := tool["description"].(string); ok {
			desc = d
		}
		if desc != "" {
			rustDoc(&b, "    ", desc)
		} else {
			fmt.Fprintf(&b, "    /// Handler for tool `%s`.\n", name)
		}
		fmt.Fprintf(&b, "    async fn %s(&self, args: Value) -> Result<Value, String>;\n", methodName)
	}
	b.WriteString("}\n\n")

	b.WriteString("/// Dispatches an MCP tools/call request to the corresponding handler method.\n")
	b.WriteString("pub async fn dispatch<H: McpHandler>(\n")
	b.WriteString("    handler: &H,\n")
	b.WriteString("    name: &str,\n")
	b.WriteString("    arguments: Value,\n")
	b.WriteString(") -> Result<Value, String> {\n")
	b.WriteString("    match name {\n")
	for _, tool := range tools {
		name := tool["name"].(string)
		methodName := snakeCase(name)
		fmt.Fprintf(&b, "        %q => handler.%s(arguments).await,\n", name, methodName)
	}
	b.WriteString("        unknown => Err(format!(\"unknown tool: {}\", unknown)),\n")
	b.WriteString("    }\n")
	b.WriteString("}\n")

	return &RustMCP{
		Crate:  crate,
		Source: []byte(b.String()),
	}, nil
}
