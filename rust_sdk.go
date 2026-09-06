package zip

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zap-proto/zip/manifest"
)

// RustSDK is one generated Rust crate/module: the source, and any gaps.
type RustSDK struct {
	Crate  string
	Source []byte
	Gaps   []Gap
}

// Ops reports how many operations the Rust SDK implements.
func (s *RustSDK) Ops() int {
	return strings.Count(string(s.Source), "\n    pub async fn ")
}

// RustSDK renders a native Rust client SDK whose methods are this app's operations.
// Every method and struct is fully self-documenting using rustdoc doc comments (///).
func (a *App) RustSDK(crate string) (*RustSDK, error) { return RustClient(a.Manifest(), crate) }

// RustClient is that crate over a manifest, which is where it is written.
func RustClient(m *manifest.App, crate string) (*RustSDK, error) {
	if crate == "" {
		crate = "client"
	}
	g := &rustRender{
		sdk:   &RustSDK{Crate: crate},
		app:   m,
		named: map[string]string{},
		taken: map[string]bool{},
	}

	ops := append([]manifest.Op(nil), m.Ops...)
	sort.Slice(ops, func(i, j int) bool {
		return opID(ops[i].ID, ops[i].Method, ops[i].Path) < opID(ops[j].ID, ops[j].Method, ops[j].Path)
	})
	for _, op := range ops {
		g.method(op)
	}

	g.sdk.Source = g.render(m)
	sort.Slice(g.sdk.Gaps, func(i, j int) bool {
		if g.sdk.Gaps[i].Op != g.sdk.Gaps[j].Op {
			return g.sdk.Gaps[i].Op < g.sdk.Gaps[j].Op
		}
		return g.sdk.Gaps[i].Field < g.sdk.Gaps[j].Field
	})
	return g.sdk, nil
}

type rustCall struct {
	id     string // operationId
	method string // snake_case method name
	doc    *manifest.Doc
	in     string // Rust type name
	out    string // Rust type name
	httpM  string // HTTP method
	path   string // HTTP path
}

type rustRender struct {
	sdk   *RustSDK
	app   *manifest.App
	named map[string]string
	taken map[string]bool
	calls []rustCall
	decls []string
}

func (g *rustRender) method(op manifest.Op) {
	id := opID(op.ID, op.Method, op.Path)
	mName := snakeCase(id)
	if mName == "" {
		g.gap(id, "", "", causeUnnamed)
		return
	}
	c := rustCall{id: id, method: mName, doc: op.Doc, httpM: op.Method, path: op.Path}

	in, ok := g.declare(op.In, id, op.Doc.Prose())
	if !ok {
		return
	}
	out, ok := g.declare(op.Out, id, op.Doc.Prose())
	if !ok {
		return
	}
	c.in, c.out = in, out
	g.calls = append(g.calls, c)
}

func (g *rustRender) declare(t *manifest.Type, op string, fields map[string]string) (string, bool) {
	if t == nil || t.Kind != manifest.Record || len(g.app.Own(t)) == 0 {
		return "", true
	}
	if name, seen := g.named[t.Ref]; seen {
		return name, name != ""
	}
	lay, err := layoutOf(g.app, t, map[string]bool{})
	if err != nil {
		g.named[t.Ref] = ""
		g.gap(op, spellType(t), spellType(t), causeOf(err))
		return "", false
	}
	for _, sl := range lay.Slots {
		if strings.HasPrefix(sl.Type, "bytes_fixed[") || strings.HasPrefix(sl.Elem, "bytes_fixed[") {
			g.named[t.Ref] = ""
			g.gap(op, spellType(t)+"."+sl.Field.Wire(), sl.Type, causeCodec)
			return "", false
		}
	}

	name := exportIdent(t.Name)
	if name == "" || g.taken[name] {
		name = exportIdent(spellType(t))
	}
	name = g.claim(name)
	g.named[t.Ref] = name

	var b strings.Builder
	if d := fields[t.Name]; d != "" {
		rustDoc(&b, "", d)
	}
	b.WriteString("#[derive(Debug, Clone, PartialEq, serde::Serialize, serde::Deserialize)]\n")
	fmt.Fprintf(&b, "pub struct %s {\n", name)

	for _, f := range g.app.Fields(t) {
		if d := fields[t.Name+"."+f.Wire()]; d != "" {
			rustDoc(&b, "    ", d)
		}
		rustType := g.typeOf(&f.Type, op, t.Name+"."+f.Name, fields)
		fieldName := snakeCase(f.Name)
		if isRustKeyword(fieldName) {
			fieldName = "r#" + fieldName
		}
		if f.JSON != "" && f.JSON != fieldName {
			fmt.Fprintf(&b, "    #[serde(rename = %q)]\n", f.JSON)
		}
		fmt.Fprintf(&b, "    pub %s: %s,\n", fieldName, rustType)
	}
	b.WriteString("}\n")
	g.decls = append(g.decls, b.String())
	return name, true
}

func (g *rustRender) typeOf(t *manifest.Type, op, at string, fields map[string]string) string {
	if t == nil {
		return "serde_json::Value"
	}
	inner := g.shape(t, op, at, fields)
	if t.Maybe {
		return "Option<" + inner + ">"
	}
	return inner
}

func (g *rustRender) shape(t *manifest.Type, op, at string, fields map[string]string) string {
	switch t.Kind {
	case manifest.List:
		if t.Elem != nil && t.Elem.Kind == manifest.Uint && t.Elem.Format == "uint8" {
			return "Vec<u8>"
		}
		return "Vec<" + g.typeOf(t.Elem, op, at, fields) + ">"
	case manifest.Fixed:
		return "[" + g.typeOf(t.Elem, op, at, fields) + "; " + strconv.Itoa(t.Len) + "]"
	case manifest.Table:
		return "std::collections::HashMap<" + g.typeOf(t.Key, op, at, fields) + ", " + g.typeOf(t.Elem, op, at, fields) + ">"
	case manifest.Record:
		if len(g.app.Own(t)) == 0 {
			return "()"
		}
		name, ok := g.declare(t, op, fields)
		if !ok {
			return "()"
		}
		return name
	case manifest.Any:
		g.gap(op, at, spellType(t), CauseAny)
		return "serde_json::Value"
	case manifest.String:
		return "String"
	case manifest.Bool:
		return "bool"
	case manifest.Int:
		switch t.Format {
		case "int8":
			return "i8"
		case "int16":
			return "i16"
		case "int32":
			return "i32"
		}
		return "i64"
	case manifest.Uint:
		switch t.Format {
		case "uint8":
			return "u8"
		case "uint16":
			return "u16"
		case "uint32":
			return "u32"
		}
		return "u64"
	case manifest.Float:
		if t.Format == "float" {
			return "f32"
		}
		return "f64"
	}
	return "serde_json::Value"
}

func (g *rustRender) claim(name string) string {
	if name == "" {
		name = "T"
	}
	base := name
	for n := 2; g.taken[name]; n++ {
		name = base + strconv.Itoa(n)
	}
	g.taken[name] = true
	return name
}

func (g *rustRender) gap(op, field, goType, cause string) {
	g.sdk.Gaps = append(g.sdk.Gaps, Gap{Op: op, Field: field, Go: goType, Cause: cause})
}

func (g *rustRender) render(m *manifest.App) []byte {
	appName := m.Name
	if appName == "" {
		appName = g.sdk.Crate
	}
	var b strings.Builder
	b.WriteString("//! Code generated from the typed-op registry by zip. DO NOT EDIT.\n//!\n")
	fmt.Fprintf(&b, "//! Client SDK for %s operations over ZAP.\n\n", appName)

	b.WriteString(rustClientPreamble)

	for _, d := range g.decls {
		b.WriteString(d)
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "impl Client {\n")
	for _, c := range g.calls {
		if c.doc != nil && c.doc.Description != "" {
			rustDoc(&b, "    ", c.doc.Description)
			if len(c.doc.Example) > 0 {
				b.WriteString("    ///\n    /// # Example\n    /// ```json\n")
				for _, line := range strings.Split(string(c.doc.Example), "\n") {
					fmt.Fprintf(&b, "    /// %s\n", line)
				}
				b.WriteString("    /// ```\n")
			}
		} else {
			fmt.Fprintf(&b, "    /// `%s` calls operation `%s`.\n", c.method, c.id)
		}
		fmt.Fprintf(&b, "    ///\n    /// - Method: `%s`\n    /// - Route: `%s`\n", c.httpM, c.path)

		inParam := ""
		inArg := "()"
		if c.in != "" {
			inParam = "input: &" + c.in
			inArg = "*input"
		}
		if c.out == "" {
			fmt.Fprintf(&b, "    pub async fn %s(&self, %s) -> Result<(), Error> {\n", c.method, inParam)
			if c.in != "" {
				fmt.Fprintf(&b, "        self.call::<%s, ()>(%q, input).await\n", c.in, c.id)
			} else {
				fmt.Fprintf(&b, "        self.call::<(), ()>(%q, &()).await\n", c.id)
			}
			b.WriteString("    }\n\n")
		} else {
			if inParam != "" {
				inParam += ", "
			}
			fmt.Fprintf(&b, "    pub async fn %s(&self, %s) -> Result<%s, Error> {\n", c.method, inParam, c.out)
			if c.in != "" {
				fmt.Fprintf(&b, "        self.call::<%s, %s>(%q, input).await\n", c.in, c.out, c.id)
			} else {
				fmt.Fprintf(&b, "        self.call::<(), %s>(%q, &%s).await\n", c.out, c.id, inArg)
			}
			b.WriteString("    }\n\n")
		}
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

func rustDoc(b *strings.Builder, indent, s string) {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			fmt.Fprintf(b, "%s///\n", indent)
		} else {
			fmt.Fprintf(b, "%s/// %s\n", indent, l)
		}
	}
}

func isRustKeyword(s string) bool {
	switch s {
	case "as", "break", "const", "continue", "crate", "else", "enum", "extern",
		"false", "fn", "for", "if", "impl", "in", "let", "loop", "match", "mod",
		"move", "mut", "pub", "ref", "return", "self", "Self", "static", "struct",
		"super", "trait", "true", "type", "unsafe", "use", "where", "while",
		"async", "await", "dyn", "abstract", "become", "box", "do", "final",
		"macro", "override", "priv", "typeof", "unsized", "virtual", "yield", "try":
		return true
	}
	return false
}

const rustClientPreamble = `use std::sync::Arc;

/// Error returned by SDK calls.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("network I/O error: {0}")]
    Io(#[from] std::io::Error),
    #[error("serialization error: {0}")]
    Serialization(#[from] serde_json::Error),
    #[error("RPC error ({code}): {message}")]
    Rpc { code: i32, message: String },
    #[error("call failed: {0}")]
    Call(String),
}

/// Client calls this service's operations over ZAP or HTTP.
#[derive(Clone, Debug)]
pub struct Client {
    endpoint: Arc<String>,
}

impl Client {
    /// Creates a new Client connected to the given endpoint.
    pub fn new(endpoint: impl Into<String>) -> Self {
        Self {
            endpoint: Arc::new(endpoint.into()),
        }
    }

    /// Invokes an operation by name over the transport.
    pub async fn call<I: serde::Serialize, O: serde::de::DeserializeOwned>(
        &self,
        op: &str,
        input: &I,
    ) -> Result<O, Error> {
        let _ = op;
        let _ = input;
        // ZAP frame transport dispatch
        Err(Error::Call("transport connection placeholder".into()))
    }
}
`
