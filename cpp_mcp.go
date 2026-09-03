package zip

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CppMCP is one generated C++20 Model Context Protocol (MCP) server header.
type CppMCP struct {
	Namespace string
	Header    []byte
}

// CppMCP renders a native C++20 Model Context Protocol (MCP) tool surface and server dispatch.
// Every tool is self-documenting with Doxygen comments and JSON input schema.
func (a *App) CppMCP(namespace string) (*CppMCP, error) {
	if namespace == "" {
		namespace = "mcp"
	}

	tools := a.MCPTools()
	var b strings.Builder
	b.WriteString("// Code generated from the typed-op registry by zip. DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// Model Context Protocol (MCP) Server tools for %s.\n\n", namespace)
	b.WriteString("#pragma once\n\n")
	b.WriteString("#include <string>\n")
	b.WriteString("#include <vector>\n")
	b.WriteString("#include <memory>\n")
	b.WriteString("#include <stdexcept>\n")
	b.WriteString("#include <nlohmann/json.hpp>\n\n")

	fmt.Fprintf(&b, "namespace %s {\n\n", namespace)

	b.WriteString("/** Tool descriptor exposed to MCP clients and agents. */\n")
	b.WriteString("struct ToolDescriptor {\n")
	b.WriteString("    std::string name;\n")
	b.WriteString("    std::string description;\n")
	b.WriteString("    nlohmann::json input_schema;\n")
	b.WriteString("};\n\n")

	b.WriteString("/** Interface implemented by service handlers to process MCP tool calls. */\n")
	b.WriteString("class McpHandler {\n")
	b.WriteString("public:\n")
	b.WriteString("    virtual ~McpHandler() = default;\n\n")

	// Sort tools by name
	sort.Slice(tools, func(i, j int) bool {
		return tools[i]["name"].(string) < tools[j]["name"].(string)
	})

	for _, tool := range tools {
		name := tool["name"].(string)
		methodName := snakeCase(name)
		desc := ""
		if d, ok := tool["description"].(string); ok {
			desc = d
		}
		if desc != "" {
			cppDoxygen(&b, "    ", desc)
		} else {
			fmt.Fprintf(&b, "    /** Handler for tool `%s`. */\n", name)
		}
		fmt.Fprintf(&b, "    virtual nlohmann::json %s(const nlohmann::json& args) = 0;\n", methodName)
	}
	b.WriteString("};\n\n")

	b.WriteString("/** Returns the list of available MCP tools for tools/list queries. */\n")
	b.WriteString("inline std::vector<ToolDescriptor> list_tools() {\n")
	b.WriteString("    return {\n")
	for _, tool := range tools {
		name := tool["name"].(string)
		desc := ""
		if d, ok := tool["description"].(string); ok {
			desc = d
		}
		schemaBytes, _ := json.Marshal(tool["inputSchema"])

		b.WriteString("        ToolDescriptor{\n")
		fmt.Fprintf(&b, "            .name = %q,\n", name)
		fmt.Fprintf(&b, "            .description = %q,\n", desc)
		fmt.Fprintf(&b, "            .input_schema = nlohmann::json::parse(%q),\n", string(schemaBytes))
		b.WriteString("        },\n")
	}
	b.WriteString("    };\n")
	b.WriteString("}\n\n")

	b.WriteString("/** Dispatches an MCP tools/call request to the corresponding handler method. */\n")
	b.WriteString("inline nlohmann::json dispatch(McpHandler& handler, const std::string& name, const nlohmann::json& arguments) {\n")
	for _, tool := range tools {
		name := tool["name"].(string)
		methodName := snakeCase(name)
		fmt.Fprintf(&b, "    if (name == %q) return handler.%s(arguments);\n", name, methodName)
	}
	b.WriteString("    throw std::runtime_error(\"unknown tool: \" + name);\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "} // namespace %s\n", namespace)

	return &CppMCP{
		Namespace: namespace,
		Header:    []byte(b.String()),
	}, nil
}
