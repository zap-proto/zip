// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

// The projector: what a manifest becomes.
//
// These are the same functions an app's own methods call — [App.OpenAPISpec] is
// OpenAPI(a.Manifest()) and nothing else — exported so a BUILD-TIME tool can run
// them over a manifest that came from somewhere other than a running Go program.
// That is the whole seam: a C++ pass over C++ source and a Rust macro over Rust
// source both write this value, and from here on there is one derivation of an
// id, a path template, a schema, a flag and a layout.

import "github.com/zap-proto/zip/manifest"

// Document is the OpenAPI 3.1 document a manifest describes. It is not named for
// the format: [OpenAPI] is already the name of the projection a service emits on
// its own command line, and one word cannot be two things.
func Document(m *manifest.App) map[string]any { return openAPI(m) }

// Tools is the MCP tool list a manifest describes, sorted by name — the list is
// SERIALIZED into a host's catalogue, and an artifact ordered by registration
// churns on an edit that changed nothing a client can see.
func Tools(m *manifest.App) []map[string]any {
	tools := make([]map[string]any, 0, len(m.Ops))
	for _, op := range m.Ops {
		tools = append(tools, mcpToolOf(m, op))
	}
	sortTools(tools)
	return tools
}

// Commands is the command tree a manifest describes. The commands carry no
// handler — a manifest describes operations, and running one is what a linked
// service adds (see [App.Commands]) or what a client sends over the wire.
func Commands(m *manifest.App) []Command {
	cmds := make([]Command, 0, len(m.Ops))
	for _, op := range m.Ops {
		c := newCommand(op.Method, op.Path, opID(op.ID, op.Method, op.Path), op.Summary, op.Doc)
		c.Args, c.Flags = bindIn(m, op)
		cmds = append(cmds, c)
	}
	sortCommands(cmds)
	return cmds
}
