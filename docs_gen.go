package zip

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zap-proto/zip/manifest"
)

// DocsBundle represents generated documentation pages formatted for @hanzo/docs.
type DocsBundle struct {
	Pages map[string]string // filename (e.g. "get-validators.mdx") -> content
	Index string            // index.mdx content
}

// DocsMarkdown generates @hanzo/docs compatible MDX documentation pages for all operations.
// It shares the core prose extracted from doc comments across Go, Rust, and C++ SDKs.
func (a *App) DocsMarkdown(title string) (*DocsBundle, error) { return Docs(a.Manifest(), title) }

// Docs is those pages over a manifest, which is where they are written.
func Docs(m *manifest.App, title string) (*DocsBundle, error) {
	if title == "" {
		title = m.Name
		if title == "" {
			title = "API Reference"
		}
	}

	bundle := &DocsBundle{
		Pages: make(map[string]string),
	}

	ops := append([]manifest.Op(nil), m.Ops...)
	sort.Slice(ops, func(i, j int) bool {
		return opID(ops[i].ID, ops[i].Method, ops[i].Path) < opID(ops[j].ID, ops[j].Method, ops[j].Path)
	})

	var index strings.Builder
	index.WriteString("---\n")
	fmt.Fprintf(&index, "title: %q\n", title)
	fmt.Fprintf(&index, "description: API documentation and SDK references for %s\n", title)
	index.WriteString("---\n\n")
	fmt.Fprintf(&index, "# %s\n\n", title)
	index.WriteString("This API is fully self-documenting and natively supported across **Go**, **Rust**, and **C++**.\n\n")
	index.WriteString("## Operations\n\n")
	index.WriteString("| Operation | Method | Route | Description |\n")
	index.WriteString("| :--- | :--- | :--- | :--- |\n")

	for _, op := range ops {
		id := opID(op.ID, op.Method, op.Path)
		doc := op.Doc
		summary := id
		if doc != nil && doc.Description != "" {
			summary = strings.Split(strings.TrimSpace(doc.Description), "\n")[0]
		}
		slug := strings.ReplaceAll(snakeCase(id), "_", "-")
		filename := slug + ".mdx"

		fmt.Fprintf(&index, "| [`%s`](./%s) | `%s` | `%s` | %s |\n", id, slug, op.Method, op.Path, summary)

		// Render operation MDX page
		var p strings.Builder
		p.WriteString("---\n")
		fmt.Fprintf(&p, "title: %q\n", id)
		fmt.Fprintf(&p, "description: %q\n", summary)
		p.WriteString("---\n\n")

		fmt.Fprintf(&p, "# %s\n\n", id)
		fmt.Fprintf(&p, "<div className=\"endpoint-badge\"><code>%s</code> <code>%s</code></div>\n\n", op.Method, op.Path)

		if doc != nil && doc.Description != "" {
			p.WriteString("## Overview\n\n")
			p.WriteString(strings.TrimSpace(doc.Description))
			p.WriteString("\n\n")
		}

		// Parameters table
		inFields := members(m, op.In, doc.Prose())
		if len(inFields) > 0 {
			p.WriteString("## Request Parameters\n\n")
			p.WriteString("| Field | Type | Required | Description |\n")
			p.WriteString("| :--- | :--- | :--- | :--- |\n")
			for _, f := range inFields {
				req := "No"
				if f.Required {
					req = "**Yes**"
				}
				desc := f.Doc
				if desc == "" {
					desc = "-"
				}
				fmt.Fprintf(&p, "| `%s` | `$%s` | %s | %s |\n", f.WireName, f.Type, req, desc)
			}
			p.WriteString("\n")
		}

		// Response fields table
		outFields := members(m, op.Out, doc.Prose())
		if len(outFields) > 0 {
			p.WriteString("## Response Fields\n\n")
			p.WriteString("| Field | Type | Description |\n")
			p.WriteString("| :--- | :--- | :--- |\n")
			for _, f := range outFields {
				desc := f.Doc
				if desc == "" {
					desc = "-"
				}
				fmt.Fprintf(&p, "| `%s` | `$%s` | %s |\n", f.WireName, f.Type, desc)
			}
			p.WriteString("\n")
		}

		// Examples
		if doc != nil && (len(doc.Example) > 0 || len(doc.Response) > 0) {
			p.WriteString("## Examples\n\n")
			if len(doc.Example) > 0 {
				p.WriteString("### Request Body\n\n```json\n")
				p.WriteString(string(doc.Example))
				p.WriteString("\n```\n\n")
			}
			if len(doc.Response) > 0 {
				p.WriteString("### Response Body\n\n```json\n")
				p.WriteString(string(doc.Response))
				p.WriteString("\n```\n\n")
			}
		}

		// Tabbed SDK invocation in Go, Rust, C++
		goMethod := exportIdent(id)
		rustMethod := snakeCase(id)
		cppMethod := snakeCase(id)

		p.WriteString("## SDK Examples\n\n")
		p.WriteString("<Tabs items={['Go', 'Rust', 'C++']}>\n")

		// Go Tab
		p.WriteString("  <Tab value=\"Go\">\n```go\n")
		fmt.Fprintf(&p, "client, err := sdk.Dial(\"127.0.0.1:9630\")\n")
		fmt.Fprintf(&p, "if err != nil {\n\tlog.Fatal(err)\n}\n")
		if len(m.Fields(op.In)) > 0 {
			fmt.Fprintf(&p, "res, err := client.%s(ctx, &sdk.%s{\n\t// ...\n})\n", goMethod, exportIdent(op.In.Name))
		} else {
			fmt.Fprintf(&p, "res, err := client.%s(ctx)\n", goMethod)
		}
		p.WriteString("```\n  </Tab>\n")

		// Rust Tab
		p.WriteString("  <Tab value=\"Rust\">\n```rust\n")
		fmt.Fprintf(&p, "let client = sdk::Client::new(\"127.0.0.1:9630\");\n")
		if len(m.Fields(op.In)) > 0 {
			fmt.Fprintf(&p, "let res = client.%s(&%s { ..Default::default() }).await?;\n", rustMethod, exportIdent(op.In.Name))
		} else {
			fmt.Fprintf(&p, "let res = client.%s().await?;\n", rustMethod)
		}
		p.WriteString("```\n  </Tab>\n")

		// C++ Tab
		p.WriteString("  <Tab value=\"C++\">\n```cpp\n")
		fmt.Fprintf(&p, "auto client = sdk::create_client(\"127.0.0.1:9630\");\n")
		if len(m.Fields(op.In)) > 0 {
			fmt.Fprintf(&p, "%s req{};\nauto res = client->%s(req);\n", exportIdent(op.In.Name), cppMethod)
		} else {
			fmt.Fprintf(&p, "auto res = client->%s();\n", cppMethod)
		}
		p.WriteString("```\n  </Tab>\n")

		p.WriteString("</Tabs>\n")

		bundle.Pages[filename] = p.String()
	}

	bundle.Index = index.String()
	return bundle, nil
}

type fieldInfo struct {
	Name     string
	WireName string
	Type     string
	Required bool
	Doc      string
}

// members is one type's fields as a page lists them: the name the wire uses,
// what it is, whether it is required, and the words its author wrote.
func members(m *manifest.App, t *manifest.Type, docs map[string]string) []fieldInfo {
	var res []fieldInfo
	for _, f := range m.Fields(t) {
		res = append(res, fieldInfo{
			Name:     f.Name,
			WireName: f.Wire(),
			Type:     spellType(&f.Type),
			Required: f.Required,
			Doc:      docs[t.Name+"."+f.Wire()],
		})
	}
	return res
}
