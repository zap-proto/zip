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
		if body := p.requestBody(op, reg); body != nil {
			obj["requestBody"] = body
		}
		if params := p.parameters(op); len(params) > 0 {
			obj["parameters"] = params
		}
		if op.Stream == streamSocket {
			obj["x-socket"] = "websocket"
		}
		resp := p.responses(op, reg)
		if op.Gated {
			if _, taken := resp["202"]; !taken {
				resp["202"] = map[string]any{
					"description": "held for approval",
					"content": map[string]any{
						"application/json": map[string]any{"schema": schemaOf(approvalType, reg, approvalProse)},
					},
				}
			}
		}
		resp["default"] = refusalResponse(reg, op.OAuth, op.Verbatim)
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

// requestBody is an op's requestBody object, nil when it takes none. Its kind is
// read off the input: a field that IS the body (bytes as sent), form fields
// (one object, its file parts binary in a multipart form), or a value (the
// input's schema), each under the media the op consumes.
func (p *projector) requestBody(op ManifestOp, reg *schemaRegistry) map[string]any {
	if !hasBody(op.Method) {
		return nil
	}
	content := map[string]any{}
	media := op.Consumes
	td := p.types[op.In]
	body := map[string]any{"required": true, "content": content}
	switch {
	case op.Raw || p.bodyField(td) != nil:
		if len(media) == 0 {
			media = []string{mimeOctet}
		}
		for _, m := range media {
			content[m] = map[string]any{"schema": binarySchema()}
		}
		// The bytes are the field, so the field's prose is the body's.
		if f := p.bodyField(td); f != nil && f.Doc != "" {
			body["description"] = f.Doc
		}
	case p.takesForm(td):
		for _, m := range media {
			content[m] = map[string]any{"schema": p.formSchema(td, m == mimeMultipart, reg)}
		}
	case p.hasRequestBody(op):
		if len(media) == 0 {
			media = []string{mimeJSON}
		}
		for _, m := range media {
			entry := map[string]any{"schema": p.schema(TypeRef{Ref: op.In}, reg)}
			if len(op.Example) > 0 {
				entry["example"] = json.RawMessage(op.Example)
			}
			content[m] = entry
		}
	default:
		return nil
	}
	return body
}

// bodyField is the input's field that IS the request body, or nil.
func (p *projector) bodyField(td *TypeDesc) *FieldDesc {
	if td == nil || td.Kind != "struct" {
		return nil
	}
	for i := range td.Fields {
		if td.Fields[i].Body {
			return &td.Fields[i]
		}
	}
	return nil
}

// takesForm reports whether the input's body is a form.
func (p *projector) takesForm(td *TypeDesc) bool {
	if td == nil || td.Kind != "struct" {
		return false
	}
	for _, f := range td.Fields {
		if f.Form != "" {
			return true
		}
	}
	return false
}

// formSchema is a form body: one object of the input's form fields under their
// form names. A file part is binary in a multipart form and its {name, type,
// bytes} object where the form arrives as JSON.
func (p *projector) formSchema(td *TypeDesc, multipart bool, reg *schemaRegistry) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range td.Fields {
		if f.Form == "" {
			continue
		}
		var fs map[string]any
		switch {
		case multipart && p.binary(f.Type):
			fs = binarySchema()
		case multipart && f.Type.List != nil && p.binary(*f.Type.List):
			fs = map[string]any{"type": "array", "items": binarySchema()}
		default:
			fs = p.schema(f.Type, reg)
		}
		if f.Doc != "" {
			fs["description"] = f.Doc
		}
		props[f.Form] = fs
		if f.Required {
			required = append(required, f.Form)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// binary reports whether a field's type is bytes where a form part carries it.
func (p *projector) binary(r TypeRef) bool {
	td := p.types[r.Ref]
	return td != nil && td.Binary
}

// parameters are an op's path, header, cookie and query parameters. The query
// carries the input's URL-borne fields whenever the body does not: for a method
// without one, and for a body that is bytes as sent or a form.
func (p *projector) parameters(op ManifestOp) []any {
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
	if td := p.types[op.In]; td != nil && td.Kind == "struct" {
		for _, f := range td.Fields {
			if f.Cookie == "" {
				continue
			}
			decls = append(decls, describe(map[string]any{
				"name": f.Cookie, "in": "cookie", "required": f.Required,
				"schema": p.schema(f.Type, nil),
			}, f.JSON, f.JSON))
		}
	}
	td := p.types[op.In]
	if !hasBody(op.Method) || p.bodyField(td) != nil || p.takesForm(td) {
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
	return decls
}

// responses are an op's answers by status: each declared status with what it
// carries. A 3xx carries no body, only the headers that send the client on. A
// stream is published at the op's first status that is not a redirect: an
// event stream with the event's type as x-events, bytes under the media the op
// produces, an upgrade as 101 with the message's type. A JSON answer is filed
// under its status; a union files each alternative under the status it states,
// several at one status being a oneOf.
func (p *projector) responses(op ManifestOp, reg *schemaRegistry) map[string]any {
	resp := map[string]any{}
	if op.Stream == streamSocket {
		entry := map[string]any{"description": "switching protocols"}
		if op.Event != "" {
			entry["x-events"] = p.schema(TypeRef{Ref: op.Event}, reg)
		}
		resp["101"] = entry
		return resp
	}
	dflt := 200
	if op.Stream == "" && !p.answers(op.Out) {
		dflt = 204
	}
	codes := statuses(op.Statuses, dflt)
	primary := codes[0]
	for _, code := range codes {
		if code < 300 || code >= 400 {
			primary = code
			break
		}
	}
	byStatus := p.alternatives(op.Out, primary)
	for _, code := range codes {
		entry := map[string]any{"description": statusText(code)}
		if h := headerDecls(op); h != nil {
			entry["headers"] = h
		}
		if code >= 300 && code < 400 {
			resp[strconv.Itoa(code)] = entry
			continue
		}
		content := map[string]any{}
		if alts := byStatus[code]; len(alts) > 0 {
			var schema map[string]any
			if len(alts) == 1 {
				schema = p.schema(alts[0], reg)
			} else {
				one := make([]any, len(alts))
				for i, a := range alts {
					one[i] = p.schema(a, reg)
				}
				schema = map[string]any{"oneOf": one}
			}
			media := map[string]any{"schema": schema}
			if len(op.Response) > 0 && code == primary {
				media["example"] = json.RawMessage(op.Response)
			}
			jsonMedia := []string{mimeJSON}
			if len(op.Produces) > 0 && op.Stream != streamBytes {
				jsonMedia = op.Produces
			}
			for _, m := range jsonMedia {
				content[m] = media
			}
		}
		if code == primary || (op.Stream == streamBytes && !p.answers(op.Out)) {
			switch op.Stream {
			case streamSSE:
				entry := map[string]any{"schema": map[string]any{"type": "string"}}
				if op.Event != "" {
					entry["x-events"] = p.schema(TypeRef{Ref: op.Event}, reg)
				}
				content[streams[streamSSE]] = entry
			case streamBytes:
				media := op.Produces
				if len(media) == 0 {
					media = []string{mimeOctet}
				}
				for _, m := range media {
					content[m] = map[string]any{"schema": binarySchema()}
				}
			}
		}
		if len(content) > 0 {
			entry["content"] = content
		}
		resp[strconv.Itoa(code)] = entry
	}
	return resp
}

// answers reports whether an op answers a JSON value.
func (p *projector) answers(out string) bool {
	td := p.types[out]
	return td != nil && (td.Name != "" || td.Kind == "union")
}

// alternatives files an op's JSON answer by status: the value under the
// primary status, or each alternative of a union under the status it states.
func (p *projector) alternatives(out string, primary int) map[int][]TypeRef {
	td := p.types[out]
	if td == nil || !p.answers(out) {
		return nil
	}
	if td.Kind != "union" {
		return map[int][]TypeRef{primary: {{Ref: out}}}
	}
	by := map[int][]TypeRef{}
	for _, alt := range td.OneOf {
		code := primary
		if a := p.types[alt.Ref]; a != nil && a.Status != 0 {
			code = a.Status
		}
		by[code] = append(by[code], alt)
	}
	if td.Name != "" && len(by) == 1 {
		// A named union answered at one status is its own schema, one
		// component every op that answers it shares.
		return map[int][]TypeRef{primary: {{Ref: out}}}
	}
	return by
}

// binarySchema is bytes sent as they are, which is what a raw body and a byte
// stream both are.
func binarySchema() map[string]any {
	return map[string]any{"type": "string", "format": "binary"}
}

// ProjectMCP is m as an MCP tool list, in name order. An op that upgrades to a
// connection is not a tool ([NotACall]); [RefusedMCP] names it.
func ProjectMCP(m Manifest) []map[string]any {
	p := newProjector(m)
	tools := make([]map[string]any, 0, len(m.Ops))
	for _, op := range m.Ops {
		if op.Stream == streamSocket {
			continue
		}
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

// RefusedMCP names the ops of m that are not tools, each with the reason: what
// tools/list's _meta says about the ops it leaves out.
func RefusedMCP(m Manifest) map[string]string {
	out := map[string]string{}
	for _, op := range m.Ops {
		if op.Stream == streamSocket {
			out[op.ID] = NotACall
		}
	}
	return out
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
			Stream:      op.Stream,
		}
		if len(op.Consumes) > 0 {
			c.Consumes = append([]string(nil), op.Consumes...)
			sort.Strings(c.Consumes)
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
// request is a flag. A declared header or cookie is a flag that rides as one;
// a body taken as sent is --body; a form field is a flag and a file part a
// --field; what is left rides the JSON body, or the query when the body is not
// JSON.
//
// The flags come in the order the document lists them — headers, cookies,
// then the query or the JSON body, then the form — so a command derived here
// and one derived from the document are the same command.
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
	add := func(name, url, kind, help string, required bool, in string) {
		if name == "-" || isParam(params, url) {
			return
		}
		flags = append(flags, Flag{
			Name:     kebab(name),
			Field:    name,
			Type:     kind,
			Help:     help,
			Required: required,
			In:       in,
		})
	}
	beside := map[string]bool{}
	for _, f := range td.Fields {
		if f.Header != "" {
			add(f.Header, f.Header, p.flagType(f.Type), f.Doc, f.Required, "header")
			beside[f.JSON] = true
		}
	}
	for _, f := range td.Fields {
		if f.Cookie != "" {
			add(f.Cookie, f.Cookie, p.flagType(f.Type), f.Doc, f.Required, "cookie")
			beside[f.JSON] = true
		}
	}
	raw, form := p.bodyField(td), p.takesForm(td)
	if hasBody(op.Method) && raw == nil && !form {
		// Every field the JSON body carries, as the document's body schema
		// lists it: a declared header or cookie rides the body too. A schema's
		// properties have no order, so both derivations sort them by name.
		n := len(flags)
		for _, f := range td.Fields {
			add(f.JSON, urlName(f), p.flagType(f.Type), p.doc(op.In, f.JSON), f.Required, "")
		}
		body := flags[n:]
		sort.Slice(body, func(i, j int) bool { return body[i].Field < body[j].Field })
		return args, flags
	}
	for _, f := range p.urlFields(op.In) {
		if beside[f.field] {
			continue
		}
		kind, _ := f.schema["type"].(string)
		add(f.name, f.name, specType(kind), p.doc(op.In, f.name), f.required, "")
	}
	if raw != nil {
		flags = append(flags, Flag{Name: "body", Field: "body", Type: "file", Help: raw.Doc, In: "body"})
	}
	if form {
		var parts []Flag
		for _, f := range td.Fields {
			if f.Form == "" {
				continue
			}
			in, kind := "form", p.flagType(f.Type)
			if p.binary(f.Type) || f.Type.List != nil && p.binary(*f.Type.List) {
				in, kind = "file", "file"
			}
			parts = append(parts, Flag{Name: kebab(f.Form), Field: f.Form, Type: kind, Help: f.Doc, Required: f.Required, In: in})
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].Field < parts[j].Field })
		flags = append(flags, parts...)
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
		case "union":
			if reg == nil || td.Name == "" {
				out := map[string]any{}
				p.fill(out, td, reg)
				return out
			}
			return reg.ref(p.define(td, reg))
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

// fill is [structSchema] over a described struct, and a union's oneOf.
func (p *projector) fill(into map[string]any, td *TypeDesc, reg *schemaRegistry) {
	if td.Kind == "union" {
		one := make([]any, len(td.OneOf))
		for i, alt := range td.OneOf {
			one[i] = p.schema(alt, reg)
		}
		into["oneOf"] = one
		return
	}
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
	case "any":
		return map[string]any{} // any JSON value, as schemaOf says of an interface
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
