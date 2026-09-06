// Code generated from the typed-op registry by zip. DO NOT EDIT.
// Model Context Protocol (MCP) Server tools for accounts.

#pragma once

#include <string>
#include <vector>
#include <memory>
#include <stdexcept>
#include <nlohmann/json.hpp>

namespace accounts {

/** Tool descriptor exposed to MCP clients and agents. */
struct ToolDescriptor {
    std::string name;
    std::string description;
    nlohmann::json input_schema;
};

/** Interface implemented by service handlers to process MCP tool calls. */
class McpHandler {
public:
    virtual ~McpHandler() = default;

    /** Handler for tool `list_`. */
    virtual nlohmann::json list(const nlohmann::json& args) = 0;
    /** Handler for tool `post`. */
    virtual nlohmann::json post(const nlohmann::json& args) = 0;
    /** Handler for tool `read`. */
    virtual nlohmann::json read(const nlohmann::json& args) = 0;
};

/** Returns the list of available MCP tools for tools/list queries. */
inline std::vector<ToolDescriptor> list_tools() {
    return {
        ToolDescriptor{
            .name = "list_",
            .description = "",
            .input_schema = nlohmann::json::parse("{\"properties\":{\"descending\":{\"type\":\"boolean\"},\"limit\":{\"format\":\"uint16\",\"type\":\"integer\"},\"offset\":{\"format\":\"uint32\",\"type\":\"integer\"}},\"type\":\"object\"}"),
        },
        ToolDescriptor{
            .name = "post",
            .description = "",
            .input_schema = nlohmann::json::parse("{\"properties\":{\"account\":{\"type\":\"string\"},\"cents\":{\"format\":\"int64\",\"type\":\"integer\"},\"memo\":{\"type\":\"string\"},\"reference\":{\"contentEncoding\":\"base64\",\"type\":\"string\"}},\"type\":\"object\"}"),
        },
        ToolDescriptor{
            .name = "read",
            .description = "",
            .input_schema = nlohmann::json::parse("{\"properties\":{\"id\":{\"type\":\"string\"}},\"type\":\"object\"}"),
        },
    };
}

/** Dispatches an MCP tools/call request to the corresponding handler method. */
inline nlohmann::json dispatch(McpHandler& handler, const std::string& name, const nlohmann::json& arguments) {
    if (name == "list_") return handler.list(arguments);
    if (name == "post") return handler.post(arguments);
    if (name == "read") return handler.read(arguments);
    throw std::runtime_error("unknown tool: " + name);
}

} // namespace accounts
