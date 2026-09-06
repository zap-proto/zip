package zip

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zap-proto/zip/manifest"
)

// CppSDK is one generated C++20 client SDK: the header content, and any gaps.
type CppSDK struct {
	Namespace string
	Header    []byte
	Gaps      []Gap
}

// Ops reports how many operations the C++ SDK implements.
func (s *CppSDK) Ops() int {
	return strings.Count(string(s.Header), "virtual ")
}

// CppSDK renders a native modern C++20 client SDK whose methods are this app's operations.
// Every method and struct is fully self-documenting using Doxygen doc comments.
func (a *App) CppSDK(namespace string) (*CppSDK, error) { return CppClient(a.Manifest(), namespace) }

// CppClient is that SDK over a manifest, which is where it is written. A C++
// service reaches it with the manifest its own source produced, so the client a
// C++ team hands out is a projection of their ops and not of a Go program.
func CppClient(m *manifest.App, namespace string) (*CppSDK, error) {
	if namespace == "" {
		namespace = "client"
	}
	g := &cppRender{
		sdk:   &CppSDK{Namespace: namespace},
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

	g.sdk.Header = g.render(m)
	sort.Slice(g.sdk.Gaps, func(i, j int) bool {
		if g.sdk.Gaps[i].Op != g.sdk.Gaps[j].Op {
			return g.sdk.Gaps[i].Op < g.sdk.Gaps[j].Op
		}
		return g.sdk.Gaps[i].Field < g.sdk.Gaps[j].Field
	})
	return g.sdk, nil
}

type cppCall struct {
	id     string
	method string
	doc    *manifest.Doc
	in     string
	out    string
	httpM  string
	path   string
}

type cppRender struct {
	sdk   *CppSDK
	app   *manifest.App
	named map[string]string
	taken map[string]bool
	calls []cppCall
	decls []string
}

func (g *cppRender) method(op manifest.Op) {
	id := opID(op.ID, op.Method, op.Path)
	mName := snakeCase(id)
	if mName == "" {
		g.gap(id, "", "", causeUnnamed)
		return
	}
	c := cppCall{id: id, method: mName, doc: op.Doc, httpM: op.Method, path: op.Path}

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

func (g *cppRender) declare(t *manifest.Type, op string, fields map[string]string) (string, bool) {
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
		cppDoxygen(&b, "", d)
	}
	fmt.Fprintf(&b, "struct %s {\n", name)

	for _, f := range g.app.Fields(t) {
		if d := fields[t.Name+"."+f.Wire()]; d != "" {
			cppDoxygen(&b, "    ", d)
		}
		cppType := g.typeOf(&f.Type, op, t.Name+"."+f.Name, fields)
		fieldName := snakeCase(f.Name)
		if isCppKeyword(fieldName) {
			fieldName += "_"
		}
		fmt.Fprintf(&b, "    %s %s{};\n", cppType, fieldName)
	}
	b.WriteString("};\n")
	g.decls = append(g.decls, b.String())
	return name, true
}

func (g *cppRender) typeOf(t *manifest.Type, op, at string, fields map[string]string) string {
	if t == nil {
		return "nlohmann::json"
	}
	inner := g.shape(t, op, at, fields)
	if t.Maybe {
		// The value may be absent, and C++ says so with an optional. A client
		// that cannot tell "zero" from "not sent" is a client that cannot round-
		// trip what the service sent it.
		return "std::optional<" + inner + ">"
	}
	return inner
}

func (g *cppRender) shape(t *manifest.Type, op, at string, fields map[string]string) string {
	switch t.Kind {
	case manifest.List:
		if t.Elem != nil && t.Elem.Kind == manifest.Uint && t.Elem.Format == "uint8" {
			return "std::vector<std::uint8_t>"
		}
		return "std::vector<" + g.typeOf(t.Elem, op, at, fields) + ">"
	case manifest.Fixed:
		return "std::array<" + g.typeOf(t.Elem, op, at, fields) + ", " + strconv.Itoa(t.Len) + ">"
	case manifest.Table:
		return "std::map<" + g.typeOf(t.Key, op, at, fields) + ", " + g.typeOf(t.Elem, op, at, fields) + ">"
	case manifest.Record:
		if len(g.app.Own(t)) == 0 {
			return "void"
		}
		name, ok := g.declare(t, op, fields)
		if !ok {
			return "void"
		}
		return name
	case manifest.Any:
		g.gap(op, at, spellType(t), CauseAny)
		return "nlohmann::json"
	case manifest.String:
		return "std::string"
	case manifest.Bool:
		return "bool"
	case manifest.Int:
		switch t.Format {
		case "int8":
			return "std::int8_t"
		case "int16":
			return "std::int16_t"
		case "int32":
			return "std::int32_t"
		}
		return "std::int64_t"
	case manifest.Uint:
		switch t.Format {
		case "uint8":
			return "std::uint8_t"
		case "uint16":
			return "std::uint16_t"
		case "uint32":
			return "std::uint32_t"
		}
		return "std::uint64_t"
	case manifest.Float:
		if t.Format == "float" {
			return "float"
		}
		return "double"
	}
	return "nlohmann::json"
}

func (g *cppRender) claim(name string) string {
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

func (g *cppRender) gap(op, field, goType, cause string) {
	g.sdk.Gaps = append(g.sdk.Gaps, Gap{Op: op, Field: field, Go: goType, Cause: cause})
}

func (g *cppRender) render(m *manifest.App) []byte {
	appName := m.Name
	if appName == "" {
		appName = g.sdk.Namespace
	}
	var b strings.Builder
	b.WriteString("// Code generated from the typed-op registry by zip. DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// Client SDK for %s operations over ZAP.\n\n", appName)
	b.WriteString("#pragma once\n\n")
	b.WriteString("#include <string>\n")
	b.WriteString("#include <vector>\n")
	b.WriteString("#include <optional>\n")
	b.WriteString("#include <array>\n")
	b.WriteString("#include <map>\n")
	b.WriteString("#include <cstdint>\n")
	b.WriteString("#include <memory>\n")
	b.WriteString("#include <stdexcept>\n")
	b.WriteString("#include <nlohmann/json.hpp>\n\n")

	fmt.Fprintf(&b, "namespace %s {\n\n", g.sdk.Namespace)

	for _, d := range g.decls {
		b.WriteString(d)
		b.WriteString("\n")
	}

	b.WriteString(cppClientPreamble)

	for _, c := range g.calls {
		if c.doc != nil && c.doc.Description != "" {
			cppDoxygen(&b, "    ", c.doc.Description)
			if len(c.doc.Example) > 0 {
				b.WriteString("    /**\n     * Example payload:\n")
				for _, line := range strings.Split(string(c.doc.Example), "\n") {
					fmt.Fprintf(&b, "     *   %s\n", line)
				}
				b.WriteString("     */\n")
			}
		} else {
			fmt.Fprintf(&b, "    /** Calls operation `%s` (%s %s). */\n", c.id, c.httpM, c.path)
		}

		retType := "void"
		if c.out != "" {
			retType = c.out
		}
		inParam := ""
		if c.in != "" {
			inParam = "const " + c.in + "& in"
		}

		fmt.Fprintf(&b, "    virtual %s %s(%s) = 0;\n\n", retType, c.method, inParam)
	}
	b.WriteString("};\n\n")
	fmt.Fprintf(&b, "} // namespace %s\n", g.sdk.Namespace)

	return []byte(b.String())
}

func cppDoxygen(b *strings.Builder, indent, s string) {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	fmt.Fprintf(b, "%s/**\n", indent)
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			fmt.Fprintf(b, "%s *\n", indent)
		} else {
			fmt.Fprintf(b, "%s * %s\n", indent, l)
		}
	}
	fmt.Fprintf(b, "%s */\n", indent)
}

func isCppKeyword(s string) bool {
	switch s {
	case "alignas", "alignof", "and", "and_eq", "asm", "atomic_cancel",
		"atomic_commit", "atomic_noexcept", "auto", "bitand", "bitor",
		"bool", "break", "case", "catch", "char", "char8_t", "char16_t",
		"char32_t", "class", "compl", "concept", "const", "consteval",
		"constexpr", "constinit", "const_cast", "continue", "co_await",
		"co_return", "co_yield", "decltype", "default", "delete", "do",
		"double", "dynamic_cast", "else", "enum", "explicit", "export",
		"extern", "false", "float", "for", "friend", "goto", "if", "inline",
		"int", "long", "mutable", "namespace", "new", "noexcept", "not",
		"not_eq", "nullptr", "operator", "or", "or_eq", "private", "protected",
		"public", "reflexpr", "register", "reinterpret_cast", "requires",
		"return", "short", "signed", "sizeof", "static", "static_assert",
		"static_cast", "struct", "switch", "synchronized", "template", "this",
		"thread_local", "throw", "true", "try", "typedef", "typeid", "typename",
		"union", "unsigned", "using", "virtual", "void", "volatile", "wchar_t",
		"while", "xor", "xor_eq":
		return true
	}
	return false
}

const cppClientPreamble = `/**
 * \brief Client interface for invoking service operations over ZAP or HTTP.
 */
class Client {
public:
    virtual ~Client() = default;
`
