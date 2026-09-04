package zip

import (
	"fmt"
	"sort"
	"strings"
)

// CppCLI is one generated C++20 CLI header.
type CppCLI struct {
	Namespace string
	Header    []byte
}

// CppCLI renders a native C++20 command-line interface with subcommands, typed flag parsing, and help text.
func (a *App) CppCLI(namespace string) (*CppCLI, error) {
	if namespace == "" {
		namespace = "cli"
	}

	cmds := a.Commands()
	var b strings.Builder
	b.WriteString("// Code generated from the typed-op registry by zip. DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// Native Command-Line Interface (CLI) for %s.\n\n", namespace)
	b.WriteString("#pragma once\n\n")
	b.WriteString("#include <string>\n")
	b.WriteString("#include <vector>\n")
	b.WriteString("#include <map>\n")
	b.WriteString("#include <memory>\n")
	b.WriteString("#include <stdexcept>\n\n")

	fmt.Fprintf(&b, "namespace %s {\n\n", namespace)

	b.WriteString("/** Command descriptor for a typed operation. */\n")
	b.WriteString("struct CommandDescriptor {\n")
	b.WriteString("    std::string service;\n")
	b.WriteString("    std::string name;\n")
	b.WriteString("    std::string operation_id;\n")
	b.WriteString("    std::string method;\n")
	b.WriteString("    std::string path;\n")
	b.WriteString("    std::string summary;\n")
	b.WriteString("    std::string example;\n")
	b.WriteString("};\n\n")

	b.WriteString("/** Interface implemented by command runners. */\n")
	b.WriteString("class CliRunner {\n")
	b.WriteString("public:\n")
	b.WriteString("    virtual ~CliRunner() = default;\n\n")

	sort.Slice(cmds, func(i, j int) bool {
		if cmds[i].Service != cmds[j].Service {
			return cmds[i].Service < cmds[j].Service
		}
		return cmds[i].Name < cmds[j].Name
	})

	seenMethods := make(map[string]bool)
	for _, c := range cmds {
		methodName := snakeCase(c.Service + "_" + c.Name)
		if seenMethods[methodName] {
			continue
		}
		seenMethods[methodName] = true
		if c.Summary != "" {
			cppDoxygen(&b, "    ", c.Summary)
		}
		fmt.Fprintf(&b, "    virtual std::string %s(const std::vector<std::string>& args, const std::map<std::string, std::string>& flags) = 0;\n", methodName)
	}
	b.WriteString("};\n\n")

	b.WriteString("/** Returns the list of registered CLI commands. */\n")
	b.WriteString("inline std::vector<CommandDescriptor> list_commands() {\n")
	b.WriteString("    return {\n")
	for _, c := range cmds {
		b.WriteString("        CommandDescriptor{\n")
		fmt.Fprintf(&b, "            .service = %q,\n", c.Service)
		fmt.Fprintf(&b, "            .name = %q,\n", c.Name)
		fmt.Fprintf(&b, "            .operation_id = %q,\n", c.OperationID)
		fmt.Fprintf(&b, "            .method = %q,\n", c.Method)
		fmt.Fprintf(&b, "            .path = %q,\n", c.Path)
		fmt.Fprintf(&b, "            .summary = %q,\n", c.Summary)
		fmt.Fprintf(&b, "            .example = %q,\n", c.Example)
		b.WriteString("        },\n")
	}
	b.WriteString("    };\n")
	b.WriteString("}\n\n")

	b.WriteString("/** Dispatches CLI arguments to the runner. */\n")
	b.WriteString("inline std::string run_cli(CliRunner& runner, const std::vector<std::string>& args) {\n")
	b.WriteString("    if (args.size() < 2) throw std::runtime_error(\"Usage: <service> <command> [flags]\");\n")
	b.WriteString("    const std::string& service = args[0];\n")
	b.WriteString("    const std::string& cmd = args[1];\n")
	b.WriteString("    std::map<std::string, std::string> flags;\n")
	b.WriteString("    std::vector<std::string> pos_args;\n")
	b.WriteString("    for (std::size_t i = 2; i < args.size(); ++i) {\n")
	b.WriteString("        if (args[i].rfind(\"--\", 0) == 0) {\n")
	b.WriteString("            auto key = args[i].substr(2);\n")
	b.WriteString("            auto eq = key.find('=');\n")
	b.WriteString("            if (eq != std::string::npos) {\n")
	b.WriteString("                flags[key.substr(0, eq)] = key.substr(eq + 1);\n")
	b.WriteString("            } else if (i + 1 < args.size() && args[i + 1].rfind(\"--\", 0) != 0) {\n")
	b.WriteString("                flags[key] = args[++i];\n")
	b.WriteString("            } else {\n")
	b.WriteString("                flags[key] = \"true\";\n")
	b.WriteString("            }\n")
	b.WriteString("        } else {\n")
	b.WriteString("            pos_args.push(args[i]);\n")
	b.WriteString("        }\n")
	b.WriteString("    }\n\n")

	seenBranches := make(map[string]bool)
	for _, c := range cmds {
		key := c.Service + ":" + c.Name
		if seenBranches[key] {
			continue
		}
		seenBranches[key] = true
		methodName := snakeCase(c.Service + "_" + c.Name)
		fmt.Fprintf(&b, "    if (service == %q && cmd == %q) return runner.%s(pos_args, flags);\n", c.Service, c.Name, methodName)
	}
	b.WriteString("    throw std::runtime_error(\"unknown command: \" + service + \" \" + cmd);\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "} // namespace %s\n", namespace)

	return &CppCLI{
		Namespace: namespace,
		Header:    []byte(b.String()),
	}, nil
}
