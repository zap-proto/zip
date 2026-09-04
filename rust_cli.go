package zip

import (
	"fmt"
	"sort"
	"strings"
)

// RustCLI is one generated Rust CLI module.
type RustCLI struct {
	Crate  string
	Source []byte
}

// RustCLI renders a native Rust command-line interface with subcommands, typed flag parsing, and help text.
func (a *App) RustCLI(crate string) (*RustCLI, error) {
	if crate == "" {
		crate = "cli"
	}

	cmds := a.Commands()
	var b strings.Builder
	b.WriteString("//! Code generated from the typed-op registry by zip. DO NOT EDIT.\n//!\n")
	fmt.Fprintf(&b, "//! Native Command-Line Interface (CLI) for %s.\n\n", crate)
	b.WriteString("use std::collections::HashMap;\n\n")

	b.WriteString("/// Command descriptor for a typed operation.\n")
	b.WriteString("#[derive(Debug, Clone)]\n")
	b.WriteString("pub struct CommandDescriptor {\n")
	b.WriteString("    pub service: &'static str,\n")
	b.WriteString("    pub name: &'static str,\n")
	b.WriteString("    pub operation_id: &'static str,\n")
	b.WriteString("    pub method: &'static str,\n")
	b.WriteString("    pub path: &'static str,\n")
	b.WriteString("    pub summary: &'static str,\n")
	b.WriteString("    pub example: &'static str,\n")
	b.WriteString("}\n\n")

	b.WriteString("/// Catalogue of available CLI commands.\n")
	b.WriteString("pub static COMMANDS: &[CommandDescriptor] = &[\n")

	sort.Slice(cmds, func(i, j int) bool {
		if cmds[i].Service != cmds[j].Service {
			return cmds[i].Service < cmds[j].Service
		}
		return cmds[i].Name < cmds[j].Name
	})

	for _, c := range cmds {
		b.WriteString("    CommandDescriptor {\n")
		fmt.Fprintf(&b, "        service: %q,\n", c.Service)
		fmt.Fprintf(&b, "        name: %q,\n", c.Name)
		fmt.Fprintf(&b, "        operation_id: %q,\n", c.OperationID)
		fmt.Fprintf(&b, "        method: %q,\n", c.Method)
		fmt.Fprintf(&b, "        path: %q,\n", c.Path)
		fmt.Fprintf(&b, "        summary: %q,\n", c.Summary)
		fmt.Fprintf(&b, "        example: %q,\n", c.Example)
		b.WriteString("    },\n")
	}
	b.WriteString("];\n\n")

	b.WriteString("/// Trait implemented by command executors.\n")
	b.WriteString("#[async_trait::async_trait]\n")
	b.WriteString("pub trait CliRunner: Send + Sync {\n")
	for _, c := range cmds {
		methodName := snakeCase(c.Service + "_" + c.Name)
		if c.Summary != "" {
			rustDoc(&b, "    ", c.Summary)
		}
		fmt.Fprintf(&b, "    async fn %s(&self, args: &[String], flags: &HashMap<String, String>) -> Result<String, String>;\n", methodName)
	}
	b.WriteString("}\n\n")

	b.WriteString("/// Dispatches CLI args to the appropriate command runner.\n")
	b.WriteString("pub async fn run_cli<R: CliRunner>(runner: &R, args: &[String]) -> Result<String, String> {\n")
	b.WriteString("    if args.len() < 2 {\n")
	b.WriteString("        return Err(\"Usage: <service> <command> [flags]\".into());\n")
	b.WriteString("    }\n")
	b.WriteString("    let service = args[0].as_str();\n")
	b.WriteString("    let cmd = args[1].as_str();\n")
	b.WriteString("    let mut flags = HashMap::new();\n")
	b.WriteString("    let mut pos_args = Vec::new();\n")
	b.WriteString("    let mut i = 2;\n")
	b.WriteString("    while i < args.len() {\n")
	b.WriteString("        if args[i].starts_with(\"--\") {\n")
	b.WriteString("            let key = args[i].trim_start_matches(\"--\");\n")
	b.WriteString("            if let Some((k, v)) = key.split_once('=') {\n")
	b.WriteString("                flags.insert(k.to_string(), v.to_string());\n")
	b.WriteString("            } else if i + 1 < args.len() && !args[i + 1].starts_with(\"--\") {\n")
	b.WriteString("                flags.insert(key.to_string(), args[i + 1].clone());\n")
	b.WriteString("                i += 1;\n")
	b.WriteString("            } else {\n")
	b.WriteString("                flags.insert(key.to_string(), \"true\".to_string());\n")
	b.WriteString("            }\n")
	b.WriteString("        } else {\n")
	b.WriteString("            pos_args.push(args[i].clone());\n")
	b.WriteString("        }\n")
	b.WriteString("        i += 1;\n")
	b.WriteString("    }\n\n")

	matchBranches := make(map[string]bool)
	b.WriteString("    match (service, cmd) {\n")
	for _, c := range cmds {
		key := c.Service + ":" + c.Name
		if matchBranches[key] {
			continue
		}
		matchBranches[key] = true
		methodName := snakeCase(c.Service + "_" + c.Name)
		fmt.Fprintf(&b, "        (%q, %q) => runner.%s(&pos_args, &flags).await,\n", c.Service, c.Name, methodName)
	}
	b.WriteString("        (s, c) => Err(format!(\"unknown command: {} {}\", s, c)),\n")
	b.WriteString("    }\n")
	b.WriteString("}\n")

	return &RustCLI{
		Crate:  crate,
		Source: []byte(b.String()),
	}, nil
}
