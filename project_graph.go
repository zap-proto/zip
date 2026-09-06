// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"sort"
	"strings"
)

// The GraphQL schema, read off a [Manifest] instead of off Go.
//
// [App.GraphQLSDL] walks reflect.Types; this walks the description of them, so
// a service whose ops were declared in Rust or C++ has the sixth projection
// too. The rules are graphql.go's own and are not restated: the field name is
// [gqlName], the type name [gqlType]'s convention, a GET is a Query and
// everything else a Mutation.
//
// TestProjectGraphQLMatchesRegistry pins the two against each other over the
// corpus, which is what says the description lost nothing a schema needs.

// ProjectGraphQL is m as a GraphQL schema, in SDL.
func ProjectGraphQL(m Manifest) string {
	p := newProjector(m)
	ops := append([]ManifestOp(nil), m.Ops...)
	sort.Slice(ops, func(i, j int) bool { return ops[i].ID < ops[j].ID })

	g := &doc{p: p, types: map[string]string{}, building: map[string]bool{}}
	var query, mutation []string
	for _, op := range ops {
		if op.ID == "" {
			// An op with no id has no name to be called by. See graphql.go.
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

// doc accumulates the named types the fields refer to. It is [sdl] over a
// description; `building` is the same cycle guard and is separate for the same
// reason.
type doc struct {
	p        *projector
	types    map[string]string
	building map[string]bool
}

func (g *doc) field(op ManifestOp) string {
	var b strings.Builder
	summary := op.Summary
	if summary == "" {
		summary = firstSentence(op.Description)
	}
	if summary != "" {
		b.WriteString(`"""` + summary + `""" `)
	}
	b.WriteString(gqlName(op.ID))
	if args := g.args(op.In); args != "" {
		b.WriteString("(" + args + ")")
	}
	b.WriteString(": " + g.ref(TypeRef{Ref: op.Out}, "Out"))
	return b.String()
}

// args flattens the input's fields into the field's arguments, for the reason
// graphql.go's does: an op's In IS its argument list.
func (g *doc) args(id string) string {
	td := g.p.types[id]
	if td == nil {
		return ""
	}
	var out []string
	for _, f := range td.Fields {
		if f.Header != "" {
			continue // ambient, not an argument. See graphql.go.
		}
		if f.JSON == "-" {
			continue
		}
		ref := g.ref(f.Type, f.Name)
		if f.Required {
			ref += "!"
		}
		out = append(out, gqlName(f.JSON)+": "+ref)
	}
	return strings.Join(out, ", ")
}

// ref names one type in GraphQL, defining it first when it is a struct.
func (g *doc) ref(r TypeRef, hint string) string {
	switch {
	case r.Ref != "":
		td := g.p.types[r.Ref]
		if td == nil {
			return "JSON"
		}
		return g.named(td, hint)
	case r.List != nil:
		if r.List.Prim == "bytes" || r.List.Prim == "u8" {
			return "String" // bytes cross as text, as they do in the document
		}
		return "[" + g.ref(*r.List, hint) + "]"
	case r.Map != nil:
		// GraphQL has no untyped node. See graphql.go.
		return "JSON"
	case r.Prim != "":
		return gqlPrim(r.Prim)
	}
	// No ref, no prim: an op that answers nothing. GraphQL has no void, so the
	// call either happened or it errored.
	return "Boolean"
}

// named is one described type as GraphQL names it.
func (g *doc) named(td *TypeDesc, hint string) string {
	switch td.Kind {
	case "struct":
		return g.define(td, hint)
	case "list":
		if td.Elem == nil {
			return "JSON"
		}
		if td.Repr == "bytes" {
			return "String"
		}
		return "[" + g.ref(*td.Elem, hint) + "]"
	case "map":
		return "JSON"
	}
	// A scalar. One that states its own JSON is a shape only the type can
	// describe, which is what JSON is the honest name for.
	if len(td.JSON) > 0 && td.Repr != "text" && td.Repr != "" {
		return "JSON"
	}
	if len(td.JSON) > 0 {
		return "JSON"
	}
	return gqlPrim(td.Repr)
}

// define writes a named object type once and returns its name.
func (g *doc) define(td *TypeDesc, hint string) string {
	name := g.name(td, hint)
	if g.building[name] {
		return name
	}
	if _, done := g.types[name]; done {
		return name
	}
	g.building[name] = true
	defer delete(g.building, name)

	var b strings.Builder
	b.WriteString("type " + name + " {\n")
	n := 0
	for _, f := range td.Fields {
		if f.JSON == "-" {
			continue
		}
		b.WriteString("  " + gqlName(f.JSON) + ": " + g.ref(f.Type, f.Name) + "\n")
		n++
	}
	if n == 0 {
		// `type X {}` is invalid SDL. See graphql.go.
		return "JSON"
	}
	b.WriteString("}\n\n")
	g.types[name] = b.String()
	return name
}

// name is [gqlType] over a description: the declared name when it has one, else
// the field or op it hangs off, which is the only name it has.
func (g *doc) name(td *TypeDesc, hint string) string {
	if td.Name != "" {
		return title(gqlName(td.Name))
	}
	if hint == "" {
		return "JSON"
	}
	return title(gqlName(hint))
}

// gqlPrim names a primitive. It is the same table graphql.go's typeRef applies
// to a reflect.Kind, keyed by the description's spelling of the same fact.
func gqlPrim(prim string) string {
	switch prim {
	case "bool":
		return "Boolean"
	case "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64":
		return "Int"
	case "f32", "f64":
		return "Float"
	case "string", "text":
		return "String"
	case "bytes":
		return "String"
	}
	if _, ok := fixedLen(prim); ok {
		return "String"
	}
	return "JSON"
}
