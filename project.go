// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"encoding/json"
	"maps"
	"sort"
	"strconv"
	"strings"
)

// The projections, read off a [Manifest] instead of off Go.
//
// These are the same documents [App.OpenAPISpec], [App.MCPTools] and
// [App.Commands] produce, derived from a description rather than from a
// reflect.Type — which is what lets a Rust or a C++ service have them. The rules
// they apply are the package's own and are not restated here: the operation id
// is [ID], the summary is [firstSentence], the path template is [Template], the
// parameters are [pathParams], the command tree is [CommandsFromSpec]. A rule
// stated twice is a rule that will be spelled two ways, and this package has the
// scar to prove it.
//
// The manifest-sourced document and the reflect-sourced one are pinned
// byte-identical over the corpus (TestProjectMatchesRegistry). That equality is
// the whole safety of the design: it says the manifest lost nothing, so a front
// end that can fill one in has everything Go has.

// ProjectOpenAPI is m as an OpenAPI 3.1 document.
func ProjectOpenAPI(m Manifest) map[string]any {
	p := newProjector(m)
	title := m.Title
	if title == "" {
		title = m.App
	}
	if title == "" {
		title = "zip API"
	}
	version := m.Version
	if version == "" {
		version = "0.0.0"
	}

	ops := append([]ManifestOp(nil), m.Ops...)
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})

	reg := newSchemaRegistry(specDefs)
	paths := map[string]map[string]any{}
	for _, op := range ops {
		path := Template(op.Path)
		if _, ok := paths[path]; !ok {
			paths[path] = map[string]any{}
		}
		obj := map[string]any{"operationId": op.ID, "summary": op.Summary}
		if op.Description != "" {
			obj["description"] = op.Description
			if op.Summary == "" {
				obj["summary"] = firstSentence(op.Description)
			}
		}
		if len(op.Tags) > 0 {
			obj["tags"] = op.Tags
		}

		if p.hasRequestBody(op) {
			media := map[string]any{"schema": p.schema(TypeRef{Ref: op.In}, reg)}
			if len(op.Example) > 0 {
				media["example"] = json.RawMessage(op.Example)
			}
			obj["requestBody"] = map[string]any{
				"required": true,
				"content":  map[string]any{"application/json": media},
			}
		}

		url := p.urlFields(op.In)
		example := exampleFields(op.Example)
		describe := func(decl map[string]any, docField, wire string) map[string]any {
			if help := p.doc(op.In, docField); help != "" {
				decl["description"] = help
			}
			if v, ok := example[wire]; ok {
				decl["example"] = v
			}
			return decl
		}

		params := pathParams(op.Path)
		decls := make([]any, 0, len(params))
		named := make(map[string]bool, len(params))
		for _, q := range params {
			named[strings.ToLower(q.Name)] = true
			named[strings.ToLower(q.Key)] = true
			decls = append(decls, describe(map[string]any{
				"name": q.Name, "in": "path", "required": true,
				"schema": url.paramSchema(q.Key),
			}, url.docKey(q.Key), q.Key))
		}
		for _, h := range p.headerFields(op.In) {
			named[strings.ToLower(h.field)] = true
			decls = append(decls, describe(map[string]any{
				"name": h.header, "in": "header", "required": h.required,
				"schema": h.schema,
			}, h.field, h.field))
		}
		if !hasBody(op.Method) {
			for _, f := range url {
				if named[strings.ToLower(f.name)] {
					continue
				}
				decls = append(decls, describe(map[string]any{
					"name": f.name, "in": "query", "required": f.required,
					"schema": f.schema,
				}, url.docKey(f.name), f.name))
			}
		}
		if len(decls) > 0 {
			obj["parameters"] = decls
		}

		resp := map[string]any{}
		if out := p.types[op.Out]; out != nil && out.Name != "" {
			media := map[string]any{"schema": p.schema(TypeRef{Ref: op.Out}, reg)}
			if len(op.Response) > 0 {
				media["example"] = json.RawMessage(op.Response)
			}
			for _, code := range statuses(op.Statuses, 200) {
				entry := map[string]any{
					"description": statusText(code),
					"content":     map[string]any{"application/json": media},
				}
				if h := headerDecls(op); h != nil {
					entry["headers"] = h
				}
				resp[strconv.Itoa(code)] = entry
			}
		} else {
			for _, code := range statuses(op.Statuses, 204) {
				entry := map[string]any{"description": statusText(code)}
				if h := headerDecls(op); h != nil {
					entry["headers"] = h
				}
				resp[strconv.Itoa(code)] = entry
			}
		}
		if op.Gated {
			if _, taken := resp["202"]; !taken {
				resp["202"] = map[string]any{
					"description": "held for approval",
					"content": map[string]any{
						"application/json": map[string]any{"schema": schemaOf(approvalType, reg, nil)},
					},
				}
			}
		}
		obj["responses"] = resp
		paths[path][strings.ToLower(op.Method)] = obj
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       title,
			"description": m.Description,
			"version":     version,
		},
		"paths":      paths,
		"components": map[string]any{"schemas": reg.defs},
	}
}

// ProjectMCP is m as an MCP tool list, in name order.
func ProjectMCP(m Manifest) []map[string]any {
	p := newProjector(m)
	tools := make([]map[string]any, 0, len(m.Ops))
	for _, op := range m.Ops {
		desc := op.Summary
		if op.Description != "" {
			desc = op.Description
		}
		annotations := map[string]any{}
		if op.Method == "GET" || op.Method == "HEAD" {
			annotations["readOnlyHint"] = true
		}
		tools = append(tools, map[string]any{
			"name":        op.ID,
			"description": desc,
			"inputSchema": p.rootSchema(op.In),
			"annotations": annotations,
		})
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i]["name"].(string) < tools[j]["name"].(string)
	})
	return tools
}

// ProjectCLI is m as a command tree.
//
// It reads the manifest rather than the document it also produces, and that is
// a deliberate difference from [CommandsFromSpec]: a document says which fields
// the BODY carries and which the URL does, and it cannot say that a body field
// opts OUT of the URL. A worker's source and its name are both spelled "script"
// — one in the path, one in the body — and a derivation that only sees the
// document drops the second as already-addressed, leaving a command that cannot
// send the body at all. The manifest carries both halves, so this one does not
// have to guess.
func ProjectCLI(m Manifest) []Command {
	p := newProjector(m)
	cmds := make([]Command, 0, len(m.Ops))
	for _, op := range m.Ops {
		c := Command{
			Service: "", Name: "",
			OperationID: op.ID,
			Summary:     op.Summary,
			Description: op.Description,
			Method:      op.Method,
			Path:        op.Path,
			Example:     op.Example,
		}
		c.Service, c.Name = commandName(op.Method, op.Path, op.ID)
		if c.Summary == "" {
			c.Summary = firstSentence(op.Description)
		}
		c.Args, c.Flags = p.bind(op)
		cmds = append(cmds, c)
	}
	sortCommands(cmds)
	return cmds
}

// bind splits an op's input into positional args and flags: the URL addresses
// the resource, so what addresses it is positional and what modifies the
// request is a flag.
func (p *projector) bind(op ManifestOp) ([]Arg, []Flag) {
	params := pathParams(op.Path)
	td := p.types[op.In]
	args := make([]Arg, 0, len(params))
	for _, q := range params {
		args = append(args, Arg{Name: q.Name, Help: p.doc(op.In, q.Name)})
	}
	if td == nil || td.Kind != "struct" {
		return args, nil
	}
	var flags []Flag
	add := func(name, url, kind string, required bool) {
		if name == "-" || isParam(params, url) {
			return
		}
		flags = append(flags, Flag{
			Name:     kebab(name),
			Field:    name,
			Type:     kind,
			Help:     p.doc(op.In, name),
			Required: required,
		})
	}
	if hasBody(op.Method) {
		for _, f := range td.Fields {
			add(f.JSON, urlName(f), p.flagType(f.Type), f.Required)
		}
	} else {
		for _, f := range p.urlFields(op.In) {
			kind, _ := f.schema["type"].(string)
			add(f.name, f.name, specType(kind), f.required)
		}
	}
	return args, flags
}

// flagType is a flag's value kind, read off the ONE schema derivation — the
// same rule [flagType] applies to a Go type, applied to a described one.
func (p *projector) flagType(r TypeRef) string {
	kind, _ := p.schema(r, nil)["type"].(string)
	return specType(kind)
}

// projector holds the type table while one document is assembled.
type projector struct {
	types map[string]*TypeDesc
}

func newProjector(m Manifest) *projector {
	p := &projector{types: make(map[string]*TypeDesc, len(m.Types))}
	for i := range m.Types {
		p.types[m.Types[i].ID] = &m.Types[i]
	}
	return p
}

// doc is the prose written for one field of a type.
func (p *projector) doc(id, field string) string {
	td := p.types[id]
	if td == nil {
		return ""
	}
	for _, f := range td.Fields {
		if f.JSON == field {
			return f.Doc
		}
	}
	return ""
}

// schema is [schemaOf] over a described type: a named struct is defined in reg
// and referred to by $ref, everything else is inlined.
func (p *projector) schema(r TypeRef, reg *schemaRegistry) map[string]any {
	switch {
	case r.Ref != "":
		td := p.types[r.Ref]
		if td == nil {
			return map[string]any{"type": "object"}
		}
		if len(td.JSON) > 0 {
			return decode(td.JSON)
		}
		switch td.Kind {
		case "list":
			return map[string]any{"type": "array", "items": p.schema(*td.Elem, reg)}
		case "map":
			return map[string]any{"type": "object", "additionalProperties": p.schema(*td.Elem, reg)}
		case "struct":
			if reg == nil {
				return map[string]any{"type": "object"}
			}
			if td.Name == "" {
				out := map[string]any{}
				p.fill(out, td, reg)
				return out
			}
			return reg.ref(p.define(td, reg))
		}
		return map[string]any{"type": "object"}
	case r.List != nil:
		return map[string]any{"type": "array", "items": p.schema(*r.List, reg)}
	case r.Map != nil:
		return map[string]any{"type": "object", "additionalProperties": p.schema(*r.Map, reg)}
	}
	return primSchema(r.Prim)
}

// define claims td's entry before filling it, which is the cycle guard.
func (p *projector) define(td *TypeDesc, reg *schemaRegistry) string {
	if name, ok := reg.byID[td.ID]; ok {
		return name
	}
	name := td.ID
	def := map[string]any{}
	reg.byID[td.ID] = name
	reg.defs[name] = def
	p.fill(def, td, reg)
	return name
}

// fill is [structSchema] over a described struct.
func (p *projector) fill(into map[string]any, td *TypeDesc, reg *schemaRegistry) {
	props := map[string]any{}
	var required []string
	for _, f := range td.Fields {
		if f.JSON == "-" {
			continue
		}
		fs := p.schema(f.Type, reg)
		if f.Doc != "" {
			fs["description"] = f.Doc
		}
		props[f.JSON] = fs
		if f.Required {
			required = append(required, f.JSON)
		}
	}
	into["type"] = "object"
	into["properties"] = props
	if len(required) > 0 {
		into["required"] = required
	}
}

// rootSchema is [rootSchemaOf] over a described type: the type inlined, with
// everything else it reaches carried alongside in $defs.
func (p *projector) rootSchema(id string) map[string]any {
	if id == "" {
		// An op that takes nothing has an EMPTY object for its arguments, not an
		// unconstrained one: a tool whose schema says "any object" invites an
		// agent to invent arguments the handler will never read.
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	reg := newSchemaRegistry(selfDefs)
	root := p.schema(TypeRef{Ref: id}, reg)
	ref, isRef := root["$ref"].(string)
	if !isRef {
		return root
	}
	name := strings.TrimPrefix(ref, reg.prefix)
	root = maps.Clone(reg.defs[name].(map[string]any))
	if reg.refs[name] == 1 {
		delete(reg.defs, name)
	}
	if len(reg.defs) > 0 {
		root["$defs"] = reg.defs
	}
	return root
}

// hasRequestBody is [hasRequestBody] over a described op.
func (p *projector) hasRequestBody(op ManifestOp) bool {
	td := p.types[op.In]
	if !hasBody(op.Method) || td == nil || td.Name == "" {
		return false
	}
	if td.Kind != "struct" {
		return true // the body IS the whole value.
	}
	named := map[string]bool{}
	for _, q := range pathParams(op.Path) {
		named[strings.ToLower(q.Name)] = true
		named[strings.ToLower(q.Key)] = true
	}
	for _, f := range td.Fields {
		if f.JSON == "-" {
			continue
		}
		if !named[strings.ToLower(urlName(f))] {
			return true
		}
	}
	return false
}

// urlFields is [urlFields] over a described input.
func (p *projector) urlFields(id string) urlFieldList {
	var out urlFieldList
	p.collectURL(id, "", map[string]bool{}, &out)
	return out
}

func (p *projector) collectURL(id, prefix string, inside map[string]bool, out *urlFieldList) {
	td := p.types[id]
	if td == nil || td.Kind != "struct" || inside[id] {
		return
	}
	inside[id] = true
	defer delete(inside, id)
	for _, f := range td.Fields {
		name := urlName(f)
		if name == "-" {
			continue
		}
		name = prefix + name
		if inner := p.record(f.Type); inner != "" {
			p.collectURL(inner, name+".", inside, out)
			continue
		}
		schema := p.urlSchema(f.Type)
		if schema == nil {
			continue
		}
		*out = append(*out, urlField{name: name, field: f.JSON, schema: schema, required: f.Required})
	}
}

// record is the id of the struct a URL names the leaves of THROUGH this field,
// or "" when the URL carries the value itself.
func (p *projector) record(r TypeRef) string {
	if r.Ref == "" {
		return ""
	}
	td := p.types[r.Ref]
	if td == nil || td.Kind != "struct" || td.Text || len(td.JSON) > 0 {
		return ""
	}
	return td.ID
}

// urlSchema is [urlSchema] over a described type: what a URL can carry into it,
// and nil for what it cannot.
func (p *projector) urlSchema(r TypeRef) map[string]any {
	switch {
	case r.Ref != "":
		td := p.types[r.Ref]
		if td == nil {
			return nil
		}
		if td.Text {
			return map[string]any{"type": "string"}
		}
		switch td.Kind {
		case "struct", "map":
			return nil
		case "list":
			if item := p.urlSchema(*td.Elem); item != nil {
				return map[string]any{"type": "array", "items": item}
			}
			return nil
		}
		if td.Repr == "bytes" || strings.HasPrefix(td.Repr, "bytes_fixed") {
			return nil
		}
		return p.schema(r, nil)
	case r.List != nil:
		if item := p.urlSchema(*r.List); item != nil {
			return map[string]any{"type": "array", "items": item}
		}
		return nil
	case r.Map != nil:
		return nil
	}
	switch r.Prim {
	case "string", "bool", "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "f32", "f64":
		return primSchema(r.Prim)
	}
	return nil
}

// headerFields is [headerFields] over a described input.
func (p *projector) headerFields(id string) []headerField {
	td := p.types[id]
	if td == nil || td.Kind != "struct" {
		return nil
	}
	var out []headerField
	for _, f := range td.Fields {
		if f.Header == "" {
			continue
		}
		out = append(out, headerField{
			header:   f.Header,
			field:    f.JSON,
			required: f.Required,
			schema:   p.schema(f.Type, nil),
		})
	}
	return out
}

// urlName is the name a URL carries a field under.
func urlName(f FieldDesc) string {
	if f.URL != "" {
		return f.URL
	}
	return f.JSON
}

// statuses is [declaredStatuses] without an op to read it off.
func statuses(declared []int, dflt int) []int {
	if len(declared) == 0 {
		return []int{dflt}
	}
	return declared
}

// headerDecls is [responseHeaderSchemas] over a described op.
func headerDecls(op ManifestOp) map[string]any {
	if len(op.ResponseHeaders) == 0 {
		return nil
	}
	out := make(map[string]any, len(op.ResponseHeaders))
	for _, name := range op.ResponseHeaders {
		out[name] = map[string]any{
			"description": "Set by " + op.Method + " " + op.Path + ".",
			"schema":      map[string]any{"type": "string"},
		}
	}
	return out
}

// primSchema is [schemaOf]'s answer for a primitive, spelled from the manifest's
// vocabulary rather than from a reflect.Kind.
func primSchema(prim string) map[string]any {
	switch prim {
	case "bool":
		return map[string]any{"type": "boolean"}
	case "string":
		return map[string]any{"type": "string"}
	case "bytes":
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64":
		return map[string]any{"type": "integer", "format": intFormat(prim)}
	case "f32":
		return map[string]any{"type": "number", "format": "float"}
	case "f64":
		return map[string]any{"type": "number", "format": "double"}
	}
	if n, ok := fixedLen(prim); ok {
		return map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "integer", "format": "uint8"},
			// The width is the type, so it is stated rather than left to a reader
			// to infer from a name it does not parse.
			"minItems": n, "maxItems": n,
		}
	}
	return map[string]any{"type": "object"}
}

// intFormat is the document's spelling of an integer's width and sign, which is
// what a fixed layout reads and "integer" alone does not carry.
func intFormat(prim string) string {
	switch prim {
	case "i64":
		return "int64"
	case "i32":
		return "int32"
	case "i16":
		return "int16"
	case "i8":
		return "int8"
	}
	return strings.Replace(prim, "u", "uint", 1)
}

// fixedLen reads N out of bytes_fixed[N].
func fixedLen(prim string) (int, bool) {
	if !strings.HasPrefix(prim, "bytes_fixed[") || !strings.HasSuffix(prim, "]") {
		return 0, false
	}
	n, err := strconv.Atoi(prim[len("bytes_fixed[") : len(prim)-1])
	return n, err == nil
}

// decode reads a schema a type stated for itself. A fresh copy every time, so
// one field's constraints cannot leak into another's through a shared map.
func decode(raw json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}
