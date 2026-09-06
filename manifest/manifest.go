// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

// What an app IS, said without Go.
//
// zip has eight projections — the REST routes, the OpenAPI document, the MCP
// tool list, the CLI, the op-call plane, the GraphQL schema, the ZAP IDL and the
// SDK — and every one of them used to read a *registeredOp, which holds
// reflect.Type and closures. That made the projections Go code about Go values,
// so a service written in another language could have none of them: the only way
// to reach a projection was to link Go and declare the op there.
//
// This package is what the projections read instead. An [App] carries exactly
// what they consult and nothing that only Go can hold: the ops, their prose, and
// the types they name, spelled as kinds and fields rather than as reflect.Type.
// Go fills it by reflection, Rust by a macro, C++ by a pass over its own source;
// each language keeps its own way of DECLARING an op and its own runtime, and
// they share the derivation from an op to a document — which is where the rules
// worth having once live: the id, the path template, the schema shape, the ZAP
// layout, the flag split.
//
// It is a VALUE and it round-trips as JSON, so the front end that produces it
// and the projector that reads it need not be the same program, the same
// process, or the same language.
package manifest

import "encoding/json"

// App is one service: what it is called, what it says about itself, the
// operations it answers, and the structs those operations name.
type App struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`

	Ops []Op `json:"ops"`

	// Structs is every struct an op reaches, under the key its [Type] refers to
	// it by. It is a table rather than a tree because a type reached by two ops
	// is ONE definition — that is what a $ref in the document and a struct in the
	// IDL both mean — and because a type that contains itself has no tree.
	Structs map[string]Struct `json:"structs,omitempty"`
}

// Op is one operation: its address, its identity, what it takes and answers, and
// the words its author wrote above it.
type Op struct {
	Method string `json:"method"`
	// Path is the router's pattern, in fiber's ":name" and "*" spelling. The
	// document's "{name}" form is derived from it — one rule, one place — so a
	// front end states the route it registers and not a second spelling of it.
	Path string `json:"path"`
	// ID is the explicit operation id, when the op declared one. Empty means the
	// default derived from method and path, which is where the rule lives.
	ID      string   `json:"id,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	// Statuses are the success codes this op may answer with. Empty means the
	// default: 200, or 204 when there is no Out.
	Statuses []int `json:"statuses,omitempty"`
	// Headers are the response headers this op may set, declared.
	Headers []string `json:"headers,omitempty"`

	// Pkg is where the handler was declared — a Go import path, a Rust module
	// path, a C++ namespace. It namespaces prose in a process that holds two
	// services answering the same address.
	Pkg string `json:"pkg,omitempty"`
	// Origin names the app this op was DECLARED in when it arrived by
	// composition, and is empty for an app's own op. It qualifies that op's named
	// types so two children may both call a type Application.
	Origin string `json:"origin,omitempty"`
	// Held says an authorizer governs this op, so the document publishes the held
	// answer beside the finished one.
	Held bool `json:"held,omitempty"`

	In  *Type `json:"in,omitempty"`
	Out *Type `json:"out,omitempty"`
	Doc *Doc  `json:"doc,omitempty"`
}

// Doc is the prose an author wrote above the handler, lifted by whatever reads
// that language's source: Go's doc pass, Rust's macro reading #[doc], C++'s
// libclang pass reading ///. One comment, every surface.
type Doc struct {
	Description string `json:"description,omitempty"`
	// Fields is a field's prose, keyed "Type.wirename" — an In and an Out can
	// both carry a "limit" and they are not the same thing.
	Fields   map[string]string `json:"fields,omitempty"`
	Example  json.RawMessage   `json:"example,omitempty"`
	Response json.RawMessage   `json:"response,omitempty"`
}

// Kind is what a value is made of. It is the vocabulary every language can
// answer in: Go reads it off reflect.Kind, C++ off the canonical type spelling,
// Rust off the type path.
type Kind string

const (
	String Kind = "string"
	Bool   Kind = "bool"
	Int    Kind = "int"
	Uint   Kind = "uint"
	Float  Kind = "float"
	Record Kind = "struct"
	List   Kind = "slice"
	Fixed  Kind = "array"
	Table  Kind = "map"
)

// Type is what a value IS, at the resolution every projection needs and no
// finer. A struct is named by Ref rather than carried inline, so a type that
// contains itself is expressible and a type reached twice is one definition.
type Type struct {
	Kind Kind `json:"kind"`
	// Name and Pkg are the declaration this type came from, when it has one.
	// They name the schema in the document, the struct in the IDL and the type in
	// an SDK; an anonymous struct or a bare []string has neither.
	Name string `json:"name,omitempty"`
	Pkg  string `json:"pkg,omitempty"`
	// Format is a number's width and sign — int32, uint64, float, double. The
	// document publishes it because a client generated from the document may
	// speak ZAP, where a field IS an offset and a width.
	Format string `json:"format,omitempty"`
	// Ref is the key into [App.Structs], for a struct and only a struct.
	Ref string `json:"ref,omitempty"`
	// Elem is what a list, an array or a map carries.
	Elem *Type `json:"elem,omitempty"`
	// Len is an array's length. A slice has none.
	Len int `json:"len,omitempty"`
	// Text says the value reads and writes itself as ONE WORD: an id, a
	// timestamp, an address. Such a value rides a URL, whatever it is made of.
	Text bool `json:"text,omitempty"`
	// States says the value writes its own JSON, so its fields are not its wire
	// form. Schema is the shape it states, when it states one; without a schema
	// the honest description is the empty one, which is what `{}` means.
	States bool           `json:"states,omitempty"`
	Schema map[string]any `json:"schema,omitempty"`
}

// Struct is a declared struct's fields, in the order the wire carries them.
type Struct struct {
	Name string  `json:"name,omitempty"`
	Pkg  string  `json:"pkg,omitempty"`
	Body []Field `json:"body"`
}

// Field is one member of a struct, with the two names a request can carry it
// under already resolved. A front end reads its own language's spelling — Go's
// `json:` and `url:` tags, C++'s ZIP_JSON annotation — so no projection parses a
// tag, and a language whose spelling is different is not a language whose
// document is different.
type Field struct {
	// Name is what the declaration calls it. JSON is what the body calls it, and
	// "-" means the body does not carry it. URL is what a path or query names it
	// under, and "-" means a URL does not carry it. Header is the header it
	// reads, when it reads one.
	Name   string `json:"name"`
	JSON   string `json:"json,omitempty"`
	URL    string `json:"url,omitempty"`
	Header string `json:"header,omitempty"`
	// Required says the handler refuses the request without it.
	Required bool `json:"required,omitempty"`
	Type     Type `json:"type"`
}

// Ref is the key a struct is filed under: its declaration, qualified by where it
// was declared, because two packages may both call a type Config. An anonymous
// struct has no such key and is filed under one made for it.
func Ref(pkg, name string) string {
	if name == "" {
		return ""
	}
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

// Fields is the body of the struct t refers to, and nil for anything else. It is
// the ONE place a Type is resolved against the table, so a projection never
// reaches into the map itself and cannot forget that a nil Type has no fields.
func (a *App) Fields(t *Type) []Field {
	if t == nil || t.Kind != Record || t.Ref == "" {
		return nil
	}
	return a.Structs[t.Ref].Body
}

// Declared reports whether t is a struct this app has a body for. A struct
// reference with no entry describes an object whose fields are unknown, which is
// what a foreign type looks like — true, and not the same as having none.
func (a *App) Declared(t *Type) bool {
	if t == nil || t.Kind != Record || t.Ref == "" {
		return false
	}
	_, ok := a.Structs[t.Ref]
	return ok
}

// Prose is a doc's field map, and nil when there is no doc — the shape every
// projection wants, so none of them repeats the nil check.
func (d *Doc) Prose() map[string]string {
	if d == nil {
		return nil
	}
	return d.Fields
}
