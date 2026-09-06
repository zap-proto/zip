// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// The registry, said without Go in it.
//
// Every projection in this package — the OpenAPI document, the MCP tool list,
// the CLI, the GraphQL schema, the ZAP IDL, the SDKs — reads a *registeredOp,
// and a registeredOp holds two reflect.Types and three closures. That is why
// they are Go's alone: a Rust service has no reflect.Type to hand them, so the
// only way it ever reached them was to have a Go program declare its ops for
// it, which is not a Rust service declaring anything.
//
// A Manifest is the same registry with the Go taken out: an op is a method, a
// path, an id, its prose and the NAMES of its input and output; a type is a
// name, a kind, and a list of fields with their wire names, their prose and
// their slots. Everything the projections read is here and nothing here is Go,
// so the projections can read this instead — and then a front end that can fill
// one in gets all of them, whatever language it is written in.
//
// There are three such front ends. Go fills a Manifest in by reflection, at run
// time, from the types it already holds ([App.Manifest]). Rust fills it in at
// COMPILE time, from a proc-macro that reads the handler's signature, its path
// attribute and its doc comments in one pass. C++ fills it in at BUILD time,
// from a libclang walk of the same declarations. None of them is privileged and
// none of them needs another.
//
// The manifest is a description, never a program: it carries no closure, no
// address and no way to run anything. What runs an op stays in the language the
// op was written in.

// ManifestVersion is the shape of the document. It is a number in the document
// itself so a projector reading one written by an older front end says so
// rather than mis-reading a field that moved.
const ManifestVersion = 1

// Manifest is one app, described so that anything can project it.
type Manifest struct {
	Manifest int `json:"manifest"`

	// App is the name the app declared for itself. It names the ZAP interface
	// and titles the document when nothing else does.
	App         string `json:"app"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`

	// Ops are this app's typed operations, in registration order — the same
	// order [App.Registry] returns, so a projection that sorts sorts once.
	Ops []ManifestOp `json:"ops"`

	// Types are every type the ops reach, each described once and referred to
	// by id. A type reached by two ops is one entry both point at, which is
	// what makes one type one schema across every projection.
	Types []TypeDesc `json:"types"`
}

// ManifestOp is one operation.
type ManifestOp struct {
	Method string `json:"method"`
	Path   string `json:"path"`

	// ID is the op's identity — the OpenAPI operationId, the MCP tool name, the
	// CLI's spelling and the key the call plane resolves. It is filled in by the
	// front end from the SAME rule [ID] states, so a manifest that names an op
	// names it the way every other surface does.
	ID string `json:"id"`

	Summary string `json:"summary,omitempty"`
	// Description is the handler's whole doc comment, minus the Example and
	// Response lines. The summary falls back to its first sentence.
	Description string `json:"description,omitempty"`

	Tags            []string `json:"tags,omitempty"`
	Statuses        []int    `json:"statuses,omitempty"`
	ResponseHeaders []string `json:"responseHeaders,omitempty"`

	// Pkg is the module the handler was declared in and Origin the app that
	// declared it when it arrived by composition. Together they say WHO owns
	// this op, which is what keeps two services' /height apart.
	Pkg    string `json:"pkg,omitempty"`
	Origin string `json:"origin,omitempty"`

	// In and Out are TypeDesc ids. Empty means the op takes or answers nothing.
	In  string `json:"in,omitempty"`
	Out string `json:"out,omitempty"`

	// Example and Response are the request and answer the doc comment wrote,
	// raw so they land in the document exactly as written.
	Example  json.RawMessage `json:"example,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`

	// Gated says an Authorizer is in force, so the document publishes the held
	// answer beside the finished one.
	Gated bool `json:"gated,omitempty"`
}

// TypeDesc is one type, described by what it is made of and by what it looks
// like on the wire — which are not always the same thing, and that is the
// reason both are here. A quoted decimal is a u64 to a fixed layout and a
// string to a JSON reader; a description that could only say one of those would
// make one of the two projections wrong.
type TypeDesc struct {
	// ID is how fields refer to this type within one manifest.
	ID string `json:"id"`
	// Name is the type's declared name, empty for an anonymous one.
	Name string `json:"name,omitempty"`
	// Pkg is the module or crate the type was declared in. It is what tells two
	// Config types apart when both reach one document.
	Pkg string `json:"pkg,omitempty"`

	// Kind is what a reader must do with it: "struct" has fields, "list" and
	// "map" hold something, "scalar" is a value. It is STRUCTURAL and says
	// nothing about the wire form — a type that states its own form says so in
	// JSON, and the two are separate because they disagree on purpose: a quoted
	// decimal is a string to a reader and eight bytes to a layout.
	Kind string `json:"kind"`

	// Spell is this type as the source language writes it. See [FieldDesc.Spell].
	Spell string `json:"spell,omitempty"`

	// JSON is the schema this type states for ITSELF — the answer a type gives
	// when its fields are not its wire form. Present for a quoted number, a
	// timestamp, a raw message. A projection that has one uses it and does not
	// look inside.
	JSON json.RawMessage `json:"json,omitempty"`

	// Text says this type reads and writes itself as one word, so a URL can
	// carry it whatever it is made of.
	Text bool `json:"text,omitempty"`

	// Repr is the fixed-layout spelling of a scalar: bool, i8..i64, u8..u64,
	// f32, f64, text, bytes, bytes_fixed[N].
	Repr string `json:"repr,omitempty"`

	// Elem is what a named list or map holds. A named slice is still a list of
	// something, and a description that forgot what would publish an array of
	// untyped objects.
	Elem *TypeRef `json:"elem,omitempty"`

	// Len is a list's fixed length, 0 for one that has none. A fixed-length
	// list of anything but bytes has no place in a layout — an element is one
	// value at one width — so the length is what tells the two apart.
	Len int `json:"len,omitempty"`

	Fields []FieldDesc `json:"fields,omitempty"`
}

// FieldDesc is one field of a struct.
type FieldDesc struct {
	// Name is the field as the source declares it; JSON is what the wire calls
	// it, "-" for a field the body does not carry.
	Name string `json:"name"`
	JSON string `json:"json"`

	// URL is the name a URL carries it under and Header the request header it
	// reads. Both empty means it rides the body alone.
	URL    string `json:"url,omitempty"`
	Header string `json:"header,omitempty"`

	Required bool   `json:"required,omitempty"`
	Doc      string `json:"doc,omitempty"`

	Type TypeRef `json:"type"`

	// Spell is this field's type as the SOURCE LANGUAGE writes it. It is the
	// one string in a manifest that is deliberately not neutral, and it is read
	// by exactly one reader: the ledger the ZAP projection writes for the person
	// who has to go and change that declaration. Sending them to
	// "map[string]info.LP" when the file says HashMap<String, Lp> is worse than
	// saying nothing.
	Spell string `json:"spell,omitempty"`
}

// TypeRef names a field's type: a primitive by its spelling, a described type
// by its id, or a container around either.
type TypeRef struct {
	Prim string   `json:"prim,omitempty"`
	Ref  string   `json:"ref,omitempty"`
	List *TypeRef `json:"list,omitempty"`
	// Len is a fixed-length list's length. See [TypeDesc.Len].
	Len int `json:"len,omitempty"`
	// Map is a map's VALUE type; a key is a string on every wire that has maps.
	Map *TypeRef `json:"map,omitempty"`
	// Opt says the value may be absent — a pointer, an Option, a nullable.
	Opt bool `json:"opt,omitempty"`
}

// Settle fills in what a front end may leave to the projector.
//
// An op's ID is the one thing a front end must NOT decide for itself. It is
// [ID]'s rule, and a rule stated in three languages is a rule that will be
// spelled three ways — so a manifest may arrive without one and this settles it,
// which is how a Rust op and a Go op at the same address come out carrying the
// same token. A front end that has an explicit id (an operation renamed on
// purpose) keeps it.
func (m *Manifest) Settle() {
	if m.Manifest == 0 {
		m.Manifest = ManifestVersion
	}
	for i := range m.Ops {
		if m.Ops[i].ID == "" {
			m.Ops[i].ID = ID(m.Ops[i].Method, m.Ops[i].Path)
		}
	}
}

// Manifest describes this app the way any front end would, so the projections
// read the same document whichever language declared the ops.
func (a *App) Manifest() Manifest {
	m := Manifest{
		Manifest:    ManifestVersion,
		App:         a.cfg.AppName,
		Title:       a.cfg.OpenAPI.Title,
		Description: a.cfg.OpenAPI.Description,
		Version:     a.cfg.OpenAPI.Version,
	}
	d := &describer{
		byType: map[reflect.Type]string{},
		taken:  map[string]bool{},
	}
	for _, op := range a.Registry() {
		doc, hasDoc := docFor(op.Pkg, op.Method, op.Path)
		mo := ManifestOp{
			Method:          op.Method,
			Path:            op.Path,
			ID:              opName(op),
			Summary:         op.Summary,
			Tags:            op.Tags,
			Statuses:        op.Statuses,
			ResponseHeaders: op.ResponseHeaders,
			Pkg:             op.Pkg,
			Origin:          op.Origin,
			In:              d.describe(op.InType, op.Origin),
			Out:             d.describe(op.OutType, op.Origin),
			Gated:           op.rule != nil && op.rule() != nil,
		}
		if hasDoc {
			mo.Description = doc.Description
			mo.Example = doc.Example
			mo.Response = doc.Response
			d.prose(doc.Fields)
		}
		m.Ops = append(m.Ops, mo)
	}
	m.Types = d.types()
	return m
}

// describer walks the types an op reaches and files each one once.
type describer struct {
	byType map[reflect.Type]string
	taken  map[string]bool
	out    []*TypeDesc
	anon   int
}

// describe files t and returns its id, or "" for a type that carries nothing —
// which is what an op with no input or no answer has.
func (d *describer) describe(t reflect.Type, origin string) string {
	if t == nil {
		return ""
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && t.NumField() == 0 && t.Name() == "" {
		return "" // struct{}: the op takes nothing.
	}
	if id, ok := d.byType[t]; ok {
		return id
	}
	id := d.id(t, origin)
	d.byType[t] = id
	td := &TypeDesc{ID: id, Name: t.Name(), Pkg: t.PkgPath(), Spell: goName(t), Text: readsText(t)}
	d.out = append(d.out, td) // claimed before the fields are walked: the cycle guard.

	if isMarshaler(t) {
		switch {
		case declaredSchema(t) != nil:
			td.JSON = rawJSON(declaredSchema(t))
		case t == timeType:
			td.JSON = rawJSON(map[string]any{"type": "string", "format": "date-time"})
		default:
			td.JSON = json.RawMessage("{}")
		}
	}

	switch t.Kind() {
	case reflect.Struct:
		td.Kind = "struct"
		d.fields(td, t, origin)
	case reflect.Slice, reflect.Array:
		td.Repr = reprOf(t)
		if t.Elem().Kind() == reflect.Uint8 {
			td.Kind = "scalar"
			d.state(td, t)
			break
		}
		td.Kind = "list"
		if t.Kind() == reflect.Array {
			td.Len = t.Len()
		}
		e := d.ref(t.Elem(), origin)
		td.Elem = &e
	case reflect.Map:
		td.Kind, td.Repr = "map", reprOf(t)
		e := d.ref(t.Elem(), origin)
		td.Elem = &e
	default:
		td.Kind, td.Repr = "scalar", reprOf(t)
		d.state(td, t)
	}
	return id
}

// state gives a value type the shape it shows on the wire, since it has no
// fields to be described by. A type that already stated its own keeps it.
func (d *describer) state(td *TypeDesc, t reflect.Type) {
	if len(td.JSON) == 0 {
		td.JSON = rawJSON(schemaOf(t, nil, nil))
	}
}

// fields describes the fields the wire carries: the struct's own and the
// promoted ones of anything it embeds, in the order a decoder resolves them.
func (d *describer) fields(td *TypeDesc, t reflect.Type, origin string) {
	for _, f := range wireFields(t) {
		fd := FieldDesc{
			Name:     f.Name,
			JSON:     jsonFieldName(f),
			URL:      urlFieldName(f),
			Header:   headerFieldName(f),
			Required: strings.Contains(f.Tag.Get("validate"), "required"),
			Type:     d.ref(f.Type, origin),
			Spell:    goName(f.Type),
		}
		if fd.URL == fd.JSON {
			fd.URL = "" // says nothing: the wire name is the URL name.
		}
		td.Fields = append(td.Fields, fd)
	}
}

// ref names a field's type without describing it twice: a primitive by its
// spelling, anything with a shape by the id it is filed under.
func (d *describer) ref(t reflect.Type, origin string) TypeRef {
	r := TypeRef{}
	if t.Kind() == reflect.Pointer {
		r.Opt = true
		t = t.Elem()
	}
	// A named type with a wire form of its own is described, not spelled: a
	// quoted decimal is not "u64" to a JSON reader and not "string" to a layout.
	if t.Name() != "" && (isMarshaler(t) || readsText(t) || t.Kind() == reflect.Struct) {
		r.Ref = d.describe(t, origin)
		return r
	}
	switch t.Kind() {
	case reflect.Struct:
		r.Ref = d.describe(t, origin)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			r.Prim = "bytes"
			if t.Kind() == reflect.Array {
				r.Prim = "bytes_fixed[" + strconv.Itoa(t.Len()) + "]"
			}
			return r
		}
		e := d.ref(t.Elem(), origin)
		r.List = &e
		if t.Kind() == reflect.Array {
			r.Len = t.Len()
		}
	case reflect.Map:
		e := d.ref(t.Elem(), origin)
		r.Map = &e
	default:
		r.Prim = primOf(t.Kind())
	}
	return r
}

// prose files the doc comments the extraction found onto the fields they
// describe. The key is "<Type>.<wire name>", which is what cmd/zipdoc wrote and
// what a Rust or C++ front end fills in directly from the comment above the
// field — so one key reaches a field's prose whichever language declared it.
func (d *describer) prose(fields map[string]string) {
	for _, td := range d.out {
		for i := range td.Fields {
			if td.Fields[i].Doc != "" {
				continue
			}
			if s := fields[td.Name+"."+td.Fields[i].JSON]; s != "" {
				td.Fields[i].Doc = s
			}
		}
	}
}

// types is the described set, in id order so one app yields one document
// whatever order its ops were registered in.
func (d *describer) types() []TypeDesc {
	out := make([]TypeDesc, 0, len(d.out))
	for _, td := range d.out {
		out = append(out, *td)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// id is the key a type is filed under: its name, qualified by the app that
// declared it when it came in by composition, then by its package when a
// different type already holds that name. It is [schemaRegistry.nameFor]'s rule,
// stated once more where there is no reflect.Type to key a map by.
func (d *describer) id(t reflect.Type, origin string) string {
	qual := ""
	if origin != "" {
		qual = origin + "."
	}
	if t.Name() == "" {
		d.anon++
		return fmt.Sprintf("#%d", d.anon)
	}
	base := qual + t.Name()
	if !d.taken[base] {
		d.taken[base] = true
		return base
	}
	if p := t.PkgPath(); p != "" {
		base = qual + p[strings.LastIndexByte(p, '/')+1:] + "." + t.Name()
	}
	for name, n := base, 2; ; n++ {
		if !d.taken[name] {
			d.taken[name] = true
			return name
		}
		name = base + strconv.Itoa(n)
	}
}

// primOf is a scalar's neutral spelling — the one a fixed layout and a schema
// both read, since the width is what a generated accessor needs and "integer"
// alone does not carry it.
func primOf(k reflect.Kind) string {
	switch k {
	case reflect.Bool:
		return "bool"
	case reflect.String:
		return "string"
	case reflect.Int8:
		return "i8"
	case reflect.Int16:
		return "i16"
	case reflect.Int32:
		return "i32"
	case reflect.Int, reflect.Int64:
		return "i64"
	case reflect.Uint8:
		return "u8"
	case reflect.Uint16:
		return "u16"
	case reflect.Uint32:
		return "u32"
	case reflect.Uint, reflect.Uint64:
		return "u64"
	case reflect.Float32:
		return "f32"
	case reflect.Float64:
		return "f64"
	}
	return "any"
}

// reprOf is a named scalar's fixed-layout spelling.
func reprOf(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "bytes"
		}
		return "list"
	case reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "bytes_fixed[" + strconv.Itoa(t.Len()) + "]"
		}
		return "list"
	case reflect.Map:
		return "bytes"
	case reflect.String:
		return "text"
	}
	return primOf(t.Kind())
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
