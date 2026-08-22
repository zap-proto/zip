package zip

// GraphQL — the SIXTH projection. The same typed-op registry (a.Registry()) that
// produces the REST routes, the OpenAPI document, the MCP tool list, the CLI and
// the op-call plane also produces a GraphQL schema, from the same two types an op
// already declares: In becomes the field's arguments, Out becomes its result.
// One value (the op), six projections (REST · OpenAPI · MCP · CLI · call · graph).
//
// # Nothing new is declared
//
// There is no schema file, no resolver map, and no second description of an
// endpoint that could disagree with the first. A field exists because an op
// exists; it disappears when the op does. That is the whole reason to derive a
// projection rather than write one — the failure this avoids is a GraphQL schema
// that still advertises an operation the service stopped serving.
//
// # A safe read is a query, everything else is a mutation
//
// GET ops become Query fields and the rest become Mutation fields, because that
// is the only signal an op gives about whether it changes anything. It is the
// same split OpenAPI publishes and the same one the CLI uses to decide whether
// to confirm, so a reader who knows one knows all three.
//
// # GraphQL is not a graph database
//
// Nothing here needs one. GraphQL is a query language over resolvers, and the
// resolver is op.direct — the handler the REST route calls, reached without a
// URL, sharing validate → authorize → run. Whatever backs the op — sqlite, a
// document store, another service over the call plane — is untouched by this
// file and invisible to it.

import (
	"reflect"
	"sort"
	"strings"
)

// GraphQLSDL renders the schema this app's ops describe, in GraphQL's schema
// definition language.
//
// SDL and not a Go type graph, because SDL is what every GraphQL client, code
// generator and editor already reads. The document is the artifact; publishing
// it is what makes the projection usable by anything that is not this process.
func (a *App) GraphQLSDL() string {
	ops := a.Registry()
	sort.Slice(ops, func(i, j int) bool { return ops[i].OperationID < ops[j].OperationID })

	g := &sdl{types: map[string]string{}, building: map[string]bool{}}
	var query, mutation []string
	for _, op := range ops {
		if op.OperationID == "" {
			// An op with no id has no name to be called by. It is reachable over
			// REST by its path, which GraphQL has no equivalent of, so it is
			// absent here rather than given an invented name that would change
			// the first time the path did.
			continue
		}
		f := g.field(op)
		if op.Method == "GET" {
			query = append(query, f)
			continue
		}
		mutation = append(mutation, f)
	}

	var b strings.Builder
	b.WriteString("# Generated from this app's typed ops. Do not edit.\n")
	b.WriteString("# Every field below is one op; there is no separate schema to drift from it.\n\n")
	writeBlock(&b, "Query", query)
	writeBlock(&b, "Mutation", mutation)

	names := make([]string, 0, len(g.types))
	for n := range g.types {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString(g.types[n])
	}
	return b.String()
}

// writeBlock emits one root type, or nothing when it has no fields. An empty
// `type Query {}` is invalid SDL, and an app with only mutations is a real thing.
func writeBlock(b *strings.Builder, name string, fields []string) {
	if len(fields) == 0 {
		return
	}
	b.WriteString("type " + name + " {\n")
	for _, f := range fields {
		b.WriteString("  " + f + "\n")
	}
	b.WriteString("}\n\n")
}

// sdl accumulates the named types the fields refer to.
//
// `building` is the cycle guard and it is separate from `types` on purpose: a
// self-referential struct is reached again while its own definition is still
// being written, so membership in `types` is not yet true and cannot be the test.
type sdl struct {
	types    map[string]string
	building map[string]bool
}

func (g *sdl) field(op *registeredOp) string {
	var b strings.Builder
	if op.Summary != "" {
		b.WriteString(`"""` + op.Summary + `""" `)
	}
	b.WriteString(gqlName(op.OperationID))
	if args := g.args(op.InType); args != "" {
		b.WriteString("(" + args + ")")
	}
	b.WriteString(": " + g.typeRef(op.OutType, "Out"))
	return b.String()
}

// args flattens the input struct's fields into the field's arguments.
//
// FLATTENED rather than a single `input:` object, because an op's In IS its
// argument list — wrapping it would add a level no other projection has and make
// the GraphQL call read differently from the same call over REST or the CLI.
func (g *sdl) args(t reflect.Type) string {
	var out []string
	for _, f := range wireFields(t) {
		if headerFieldName(f) != "" {
			// A header is AMBIENT, not an argument. Over REST its value comes from
			// the request — set by whatever runs in front of the handler — so
			// publishing it as an argument would invite a client to supply its own
			// instead. The executor refuses it by the same rule.
			continue
		}
		name := jsonFieldName(f)
		if name == "-" {
			continue
		}
		ref := g.typeRef(f.Type, f.Name)
		if strings.Contains(f.Tag.Get("validate"), "required") {
			ref += "!"
		}
		out = append(out, gqlName(name)+": "+ref)
	}
	return strings.Join(out, ", ")
}

// typeRef names t in GraphQL, defining it first when it is a struct.
func (g *sdl) typeRef(t reflect.Type, hint string) string {
	t = deref(t)
	if t == nil {
		// An op with no Out still answers — it answers nothing. GraphQL has no
		// void, so it is Boolean: the call either happened or it errored.
		return "Boolean"
	}
	if t == timeType {
		// time.Time writes itself as an RFC 3339 string, so a string is what a
		// caller sees. Describing it by its fields would describe something that
		// never crosses the wire.
		return "String"
	}
	if isMarshaler(t) {
		// The type writes its own JSON, so its Go fields say nothing about the
		// shape that arrives. JSON is the honest name for a value only the type
		// itself can describe.
		return "JSON"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "Boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "Int"
	case reflect.Float32, reflect.Float64:
		return "Float"
	case reflect.String:
		return "String"
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "String" // []byte crosses as text, as it does in the JSON document
		}
		return "[" + g.typeRef(t.Elem(), hint) + "]"
	case reflect.Struct:
		return g.define(t, hint)
	case reflect.Map, reflect.Interface:
		// GraphQL has no untyped node. Naming this JSON is honest about what it
		// is — an opaque blob the schema cannot describe — rather than inventing
		// a shape a client would then rely on.
		return "JSON"
	}
	return "JSON"
}

// define writes a named object type once and returns its name.
func (g *sdl) define(t reflect.Type, hint string) string {
	name := gqlType(t, hint)
	if g.building[name] {
		return name // reached while still being written: the cycle guard
	}
	if _, done := g.types[name]; done {
		return name
	}
	g.building[name] = true
	defer delete(g.building, name)

	var b strings.Builder
	b.WriteString("type " + name + " {\n")
	n := 0
	for _, f := range wireFields(t) {
		fname := jsonFieldName(f)
		if fname == "-" {
			continue
		}
		b.WriteString("  " + gqlName(fname) + ": " + g.typeRef(f.Type, f.Name) + "\n")
		n++
	}
	if n == 0 {
		// A type with no exported fields is not expressible: `type X {}` is
		// invalid SDL. It becomes JSON rather than an empty type nobody can query.
		return "JSON"
	}
	b.WriteString("}\n\n")
	g.types[name] = b.String()
	return name
}

func deref(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// gqlType names a struct. The Go type name is used when it has one; an anonymous
// struct borrows the field or op it hangs off, which is the only name it has.
//
// Capitalised, because a GraphQL type conventionally is, and a schema that reads
// as though somebody wrote it is the point of generating one. openapi's sanitize
// is not reused here: it lower-cases, which is right for a component key and
// wrong for a type.
func gqlType(t reflect.Type, hint string) string {
	if n := t.Name(); n != "" {
		return title(gqlName(n))
	}
	if hint == "" {
		return "JSON"
	}
	return title(gqlName(hint))
}

// title upper-cases the first letter and nothing else — the rest of the name is
// the Go author's, and re-casing it would turn UserID into Userid.
func title(s string) string {
	if s == "" {
		return s
	}
	if c := s[0]; c >= 'a' && c <= 'z' {
		return string(c-32) + s[1:]
	}
	return s
}

// gqlName makes a field name GraphQL accepts: /[_A-Za-z][_0-9A-Za-z]*/.
func gqlName(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9' && i > 0:
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "_"
	}
	return out
}
