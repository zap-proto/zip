package zip

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/zap-proto/zip/internal/zapenc"
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
func (a *App) CppSDK(namespace string) (*CppSDK, error) {
	if namespace == "" {
		namespace = "client"
	}
	g := &cppRender{
		sdk:   &CppSDK{Namespace: namespace},
		named: map[reflect.Type]string{},
		taken: map[string]bool{},
	}

	ops := append([]*registeredOp(nil), a.Registry()...)
	sort.Slice(ops, func(i, j int) bool { return opName(ops[i]) < opName(ops[j]) })
	for _, op := range ops {
		g.method(op)
	}

	g.sdk.Header = g.render(a)
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
	doc    Doc
	hasDoc bool
	in     string
	out    string
	httpM  string
	path   string
}

type cppRender struct {
	sdk   *CppSDK
	named map[reflect.Type]string
	taken map[string]bool
	calls []cppCall
	decls []string
}

func (g *cppRender) method(op *registeredOp) {
	id := opName(op)
	doc, hasDoc := docFor(op.Pkg, op.Method, op.Path)
	mName := snakeCase(id)
	if mName == "" {
		g.gap(id, "", "", causeUnnamed)
		return
	}
	c := cppCall{
		id:     id,
		method: mName,
		doc:    doc,
		hasDoc: hasDoc,
		httpM:  op.Method,
		path:   op.Path,
	}

	in, ok := g.declare(op.InType, id, docFields(hasDoc, doc))
	if !ok {
		return
	}
	out, ok := g.declare(op.OutType, id, docFields(hasDoc, doc))
	if !ok {
		return
	}
	c.in, c.out = in, out
	g.calls = append(g.calls, c)
}

func (g *cppRender) declare(t reflect.Type, op string, fields map[string]string) (string, bool) {
	t = deref(t)
	if t == nil || t.Kind() != reflect.Struct || t.NumField() == 0 {
		return "", true
	}
	if name, seen := g.named[t]; seen {
		return name, name != ""
	}
	shape, err := zapenc.LayoutOf(t)
	if err != nil {
		g.named[t] = ""
		g.gap(op, goName(t), t.String(), causeOf(err))
		return "", false
	}
	for _, s := range shape.Slots {
		if strings.HasPrefix(s.Type, "bytes_fixed[") || strings.HasPrefix(s.Elem, "bytes_fixed[") {
			g.named[t] = ""
			g.gap(op, goName(t)+"."+s.Name, s.Type, causeCodec)
			return "", false
		}
	}

	name := exportIdent(t.Name())
	if name == "" || g.taken[name] {
		name = exportIdent(goName(t))
	}
	name = g.claim(name)
	g.named[t] = name

	var b strings.Builder
	if d := fields[t.Name()]; d != "" {
		cppDoxygen(&b, "", d)
	}
	fmt.Fprintf(&b, "struct %s {\n", name)

	var fieldNames []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		jsonName := jsonFieldName(f)
		if d := fields[t.Name()+"."+jsonName]; d != "" {
			cppDoxygen(&b, "    ", d)
		}
		cppType := g.typeOf(f.Type, op, t.Name()+"."+f.Name, fields)
		fieldName := snakeCase(f.Name)
		if isCppKeyword(fieldName) {
			fieldName += "_"
		}
		fieldNames = append(fieldNames, fieldName)
		fmt.Fprintf(&b, "    %s %s{};\n", cppType, fieldName)
	}
	b.WriteString("};\n")
	g.decls = append(g.decls, b.String())
	return name, true
}

func (g *cppRender) typeOf(t reflect.Type, op, at string, fields map[string]string) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "std::optional<" + g.typeOf(t.Elem(), op, at, fields) + ">"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "std::vector<std::uint8_t>"
		}
		return "std::vector<" + g.typeOf(t.Elem(), op, at, fields) + ">"
	case reflect.Array:
		return "std::array<" + g.typeOf(t.Elem(), op, at, fields) + ", " + strconv.Itoa(t.Len()) + ">"
	case reflect.Map:
		return "std::map<" + g.typeOf(t.Key(), op, at, fields) + ", " + g.typeOf(t.Elem(), op, at, fields) + ">"
	case reflect.Struct:
		if t.NumField() == 0 {
			return "void"
		}
		name, ok := g.declare(t, op, fields)
		if !ok {
			return "void"
		}
		return name
	case reflect.Interface:
		g.gap(op, at, t.String(), CauseAny)
		return "nlohmann::json"
	case reflect.String:
		return "std::string"
	case reflect.Bool:
		return "bool"
	case reflect.Int8:
		return "std::int8_t"
	case reflect.Int16:
		return "std::int16_t"
	case reflect.Int32:
		return "std::int32_t"
	case reflect.Int, reflect.Int64:
		return "std::int64_t"
	case reflect.Uint8:
		return "std::uint8_t"
	case reflect.Uint16:
		return "std::uint16_t"
	case reflect.Uint32:
		return "std::uint32_t"
	case reflect.Uint, reflect.Uint64:
		return "std::uint64_t"
	case reflect.Float32:
		return "float"
	case reflect.Float64:
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

func (g *cppRender) render(a *App) []byte {
	appName := a.cfg.AppName
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
		if c.hasDoc && c.doc.Description != "" {
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
