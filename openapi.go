package zip

import (
	"cmp"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
	"github.com/zap-proto/zip/internal/jsontag"
)

// SpecPath and DocsPath are where an app serves its own OpenAPI document and
// the interactive page over it. Both are zip's control plane — a projection of
// the app, not a door its owner wrote — so neither appears in a [Declaration]
// and a host serves its own.
const (
	SpecPath = "/.well-known/openapi.json"
	DocsPath = "/docs"
)

// OpenAPIConfig configures the auto-generated /.well-known/openapi.json
// endpoint zip serves when typed handlers are registered.
type OpenAPIConfig struct {
	// Title appears in the OpenAPI info block.
	Title string
	// Description appears in the OpenAPI info block.
	Description string
	// Version appears in the OpenAPI info block (e.g. "v1.0.0").
	Version string
	// Disabled suppresses the /.well-known/openapi.json route and /docs.
	Disabled bool
}

// installOpenAPIRoutes wires /.well-known/openapi.json and /docs.
// Called from Listen / Serve. Idempotent if there are no typed ops.
func (a *App) installOpenAPIRoutes() {
	if a.cfg.OpenAPI.Disabled || len(a.registry) == 0 {
		return
	}
	spec := a.buildOpenAPI()
	specJSON, _ := jsonenc.Marshal(spec)

	a.control(fiber.MethodGet, SpecPath, func(fc fiber.Ctx) error {
		fc.Set("Content-Type", "application/json")
		return fc.Send(specJSON)
	})
	a.control(fiber.MethodGet, DocsPath, func(fc fiber.Ctx) error {
		fc.Set("Content-Type", "text/html; charset=utf-8")
		return fc.SendString(swaggerHTML)
	})
}

// buildOpenAPI walks the registered typed ops and builds an OpenAPI 3.1
// spec as a plain map (json.Marshal serializes anything map-shaped).
func (a *App) buildOpenAPI() map[string]any {
	cfg := a.cfg.OpenAPI
	if cfg.Title == "" {
		cfg.Title = a.cfg.AppName
	}
	if cfg.Title == "" {
		cfg.Title = "zip API"
	}
	if cfg.Version == "" {
		cfg.Version = "0.0.0"
	}

	paths := map[string]map[string]any{}
	// One registry for the whole document: a type reached by two ops is one
	// definition in components.schemas that both point at.
	reg := newSchemaRegistry(specDefs)

	// Sort ops by path,method for deterministic output.
	ops := append([]*registeredOp{}, a.registry...)
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})

	for _, op := range ops {
		// Whose types these are. Empty for this app's own ops, so a document
		// with nothing grafted into it is byte-identical to the one before
		// Graft existed.
		reg.origin = op.Origin

		path := op.Path
		// OpenAPI uses {name} for path params; fiber uses :name. Translate.
		path = strings.ReplaceAll(path, "/:", "/{")
		if strings.Contains(path, "{") && !strings.Contains(path, "}") {
			// Was ":name" -> "{name" needs the closing brace.
			path = closeColonParams(op.Path)
		}

		if _, ok := paths[path]; !ok {
			paths[path] = map[string]any{}
		}
		// opName is the ONE place the id rule lives, shared with the MCP tool
		// list, the op-call plane and the Authorizer's Op — so an operation is
		// addressed by the same token whichever projection you came through.
		opObj := map[string]any{
			"operationId": opName(op),
			"summary":     op.Summary,
		}
		// Prose and examples extracted from the source by cmd/zipdoc. Absent
		// when the generator has not run, which degrades to the schema-only
		// spec rather than failing — a spec without descriptions is still a
		// usable spec.
		doc, hasDoc := docFor(op.Method, op.Path)
		if hasDoc {
			if doc.Description != "" {
				opObj["description"] = doc.Description
			}
			if op.Summary == "" {
				opObj["summary"] = firstSentence(doc.Description)
			}
		}
		if len(op.Tags) > 0 {
			opObj["tags"] = op.Tags
		}

		// Request body.
		if hasRequestBody(op) {
			media := map[string]any{"schema": schemaOf(op.InType, reg, docFields(hasDoc, doc))}
			// An example is what makes a spec explorable — it is the difference
			// between a reference someone reads and one they can press "try it"
			// on.
			if hasDoc && len(doc.Example) > 0 {
				media["example"] = json.RawMessage(doc.Example)
			}
			opObj["requestBody"] = map[string]any{
				"required": true,
				"content":  map[string]any{"application/json": media},
			}
		}

		// Path parameters. OpenAPI requires every templated segment to be declared,
		// so they are derived from the route pattern itself — the same string the
		// router matches on, so the spec cannot describe a parameter the route does
		// not have (or omit one it does).
		// A parameter's prose lives with the field it binds to, under the same
		// "<Type>.<field>" key the in-process CLI reads. Looking it up here
		// rather than restating it is what keeps a generated client and a
		// linked-in one describing the same argument the same way — the
		// alternative is a spec that silently has nothing to say about an
		// argument the registry documents fine.
		fields := docFields(hasDoc, doc)
		inName := typeName(op.InType)
		// A parameter's example is that field's value in the op's OWN example —
		// the one the doc comment already wrote, split across the parameters
		// that carry it. A bodyless op has no requestBody for the example to
		// live in, and an example that only survives for methods with a body is
		// an example missing from every GET and DELETE in the document.
		example := exampleFields(doc.Example)
		describe := func(decl map[string]any, field string) map[string]any {
			if help := fields[inName+"."+field]; help != "" {
				decl["description"] = help
			}
			if v, ok := example[field]; ok {
				decl["example"] = v
			}
			return decl
		}

		// A parameter's TYPE is the type of the field it binds to, wherever the
		// URL carried it from. bindURL is one binder over one set of fields, so
		// the document reads that one set too — declaring every path param a
		// string while consulting the input for query params described the same
		// value two different ways depending on which half of the URL it rode in.
		url := urlFields(op.InType)
		params := colonParams(op.Path)
		decls := make([]any, 0, len(params))
		named := make(map[string]bool, len(params))
		for _, p := range params {
			named[strings.ToLower(p)] = true
			decls = append(decls, describe(map[string]any{
				"name": p, "in": "path", "required": true,
				"schema": url.paramSchema(p),
			}, p))
		}
		// Query parameters. A bodyless method binds its input from the URL
		// (typed.go bindURL), so every In field that is NOT already a path
		// segment is reachable as `?field=` — and the document has to say so, or
		// it describes a route nobody can call correctly. Declared only where
		// there is no requestBody, because that is exactly where the binder
		// treats the URL as the whole input.
		if !hasBody(op.Method) {
			for _, f := range url {
				if named[strings.ToLower(f.name)] {
					continue
				}
				decls = append(decls, describe(map[string]any{
					"name": f.name, "in": "query", "required": f.required,
					"schema": f.schema,
				}, f.name))
			}
		}
		if len(decls) > 0 {
			opObj["parameters"] = decls
		}

		// The success response, keyed on the status the op DECLARED — the whole
		// reason WithStatus is on the op rather than set per request: a 201 that
		// only reached the wire would leave every generated client expecting a
		// 200 the service never sends.
		if op.OutType != nil && typeName(op.OutType) != "" {
			respMedia := map[string]any{"schema": schemaOf(op.OutType, reg, docFields(hasDoc, doc))}
			if hasDoc && len(doc.Response) > 0 {
				respMedia["example"] = json.RawMessage(doc.Response)
			}
			code := cmp.Or(op.Status, 200)
			opObj["responses"] = map[string]any{
				strconv.Itoa(code): map[string]any{
					"description": statusText(code),
					"content":     map[string]any{"application/json": respMedia},
				},
			}
		} else {
			code := cmp.Or(op.Status, 204)
			opObj["responses"] = map[string]any{
				strconv.Itoa(code): map[string]any{"description": statusText(code)},
			}
		}

		paths[path][strings.ToLower(op.Method)] = opObj
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       cfg.Title,
			"description": cfg.Description,
			"version":     cfg.Version,
		},
		"paths": paths,
		"components": map[string]any{
			"schemas": reg.defs,
		},
	}
}

func defaultOpID(method, path string) string {
	clean := strings.ReplaceAll(path, "/", "_")
	clean = strings.ReplaceAll(clean, "{", "")
	clean = strings.ReplaceAll(clean, "}", "")
	clean = strings.ReplaceAll(clean, ":", "")
	return strings.ToLower(method) + clean
}

// hasBody reports whether a method carries a JSON request body ON THE WIRE. It
// is the ONE place that rule lives, and since v1.18.0 it is the only place:
//
//   - typed.go reads the request body only when it says so;
//   - openapi.go declares a requestBody, else query parameters;
//   - cli.go's Remote.Invoke sends a body, else a query string;
//   - cli.go's bindIn offers flags for what that shape can actually carry.
//
// It is about the WIRE, not about the op. op.invoke always decodes the JSON it
// is handed, because addressing an op by name (MCP tools/call, zip.Call, a
// command) has no URL to carry half the input in — there the arguments object
// IS the whole input, whatever the method would have done over HTTP.
//
// DELETE carries no body. It used to, in the route and in the CLI's remote
// invoker, while the document said it did not — so a typed Delete read an input
// no generated client would ever send. Three spellings, one of them the
// document's; they are now one predicate.
func hasBody(method string) bool {
	switch method {
	case "GET", "HEAD", "DELETE":
		return false
	}
	return true
}

// hasRequestBody reports whether an op PUBLISHES a request body: its method
// carries one, its input has a name to describe, and the input has at least one
// field the URL does not already carry.
//
// That last clause is the one that was missing. POST /v1/things/:id/verify binds
// its whole input from the path, so the requestBody the document declared had
// exactly one property — the path param — and it was marked required. Every SDK
// generated from that document gained a phantom argument: an object the caller
// must construct, to repeat a value it already passes in the URL.
//
// It is a refinement OF [hasBody], not a rival to it. The wire is untouched: the
// route still reads the body for a method that carries one, and bindURL binds the
// path last, so a body that repeated a path param never won anyway. The only
// thing that changes is what the document, and therefore every client generated
// from it, believes it has to send.
func hasRequestBody(op *registeredOp) bool {
	if !hasBody(op.Method) || typeName(op.InType) == "" {
		return false
	}
	t := op.InType
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return true // the body IS the whole value — a list, a raw message
	}
	named := map[string]bool{}
	for _, p := range colonParams(op.Path) {
		named[strings.ToLower(p)] = true
	}
	// wireFields, not NumField: an embedded struct's fields ARE in the body, and
	// asking the outer type alone declared no body for an input whose own fields
	// were all path params — the phantom body's mirror image.
	for _, f := range wireFields(t) {
		// A field is in the body when `json:` puts it there, and is already carried
		// when `url:` binds it to a segment the route matched on. The two tags
		// answer two questions, so both are asked.
		if jsonFieldName(f) == "-" {
			continue
		}
		if !named[strings.ToLower(urlFieldName(f))] {
			return true
		}
	}
	return false
}

// urlField is one URL-bindable input field: the name a caller writes in the URL,
// the schema of the value, and whether the handler refuses to run without it.
type urlField struct {
	name     string
	schema   map[string]any
	required bool
}

// urlFields lists the top-level scalar fields of an op's input — the exact set
// bindURL can fill, whether the value arrives as a path segment or a query key.
// ONE list serves both because it is ONE binder: a path param and a query param
// are the same kind of value, so the document describes them from the same
// place. Non-scalars are omitted because the binder cannot fill them either, so
// naming them would promise a parameter that silently does nothing.
func urlFields(t reflect.Type) urlFieldList {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out urlFieldList
	for _, f := range wireFields(t) {
		name := urlFieldName(f)
		if name == "-" {
			continue // on the wire, but not in the URL.
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			out = append(out, urlField{
				name:   name,
				schema: schemaOf(ft, nil, nil),
				// The same `validate:"required"` that makes a body field
				// required in its schema makes a URL-borne one required in its
				// parameter. The handler refuses the request either way; a
				// document that called it optional was describing a call that
				// cannot succeed, and every generated client made the argument
				// optional to match.
				required: strings.Contains(f.Tag.Get("validate"), "required"),
			})
		}
	}
	return out
}

type urlFieldList []urlField

// paramSchema is the schema of the field a URL-borne name binds to, matched the
// way bindURL matches it. A name the input does not declare is a string: the
// router still matches that segment and the wire still carried text, and bindURL
// drops it for the same reason the document cannot type it — there is no field.
func (l urlFieldList) paramSchema(name string) map[string]any {
	for _, f := range l {
		if strings.EqualFold(f.name, name) {
			return f.schema
		}
	}
	return map[string]any{"type": "string"}
}

// colonParams lists the ":name" params of a fiber route pattern, in order.
func colonParams(path string) []string {
	var out []string
	for _, p := range strings.Split(path, "/") {
		if strings.HasPrefix(p, ":") && len(p) > 1 {
			out = append(out, p[1:])
		}
	}
	return out
}

func closeColonParams(path string) string {
	// Convert ":name" segments to "{name}".
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "{" + p[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

func typeName(t reflect.Type) string {
	if t == nil {
		return ""
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "" {
		return ""
	}
	return t.Name()
}

// Where a projection keeps the definitions its schemas refer to. An OpenAPI
// document has one place for them; a schema sent on its own carries its own.
const (
	specDefs = "#/components/schemas/"
	selfDefs = "#/$defs/"
)

// schemaRegistry is where a named struct type is described ONCE. Every use of it
// is a $ref into the registry — which is also the cycle guard: the entry is
// claimed BEFORE the type's fields are walked, so a type that contains itself
// finds the definition already in flight instead of recursing forever. Without
// it a self-referential In type overflows the stack while the document is built
// AND while the tools are listed, which is fatal — a stack overflow is not
// recoverable, so the service simply cannot start.
//
// prefix is the JSON Pointer that reaches defs. The projection owns where its
// definitions live; the derivation that fills them is the same one either way.
type schemaRegistry struct {
	prefix string
	defs   map[string]any          // name → definition
	names  map[reflect.Type]string // type → the name it is defined under
	refs   map[string]int          // name → how many $refs point at it

	// origin is the app that DECLARED the op currently being described, when
	// that op arrived through [App.Graft] — empty for the host's own. It
	// qualifies the names of the types that op reaches, so a composed document
	// can carry two apps' Application without one overwriting the other. See
	// nameFor. Set by buildOpenAPI, once per op; a type is qualified by
	// whichever origin reached it FIRST and keeps that name everywhere, which
	// is what makes one type one schema across the whole document.
	origin string
}

func newSchemaRegistry(prefix string) *schemaRegistry {
	return &schemaRegistry{
		prefix: prefix,
		defs:   map[string]any{},
		names:  map[reflect.Type]string{},
		refs:   map[string]int{},
	}
}

// define describes t in the registry, if it is not there already, and returns
// the name it is described under. The entry is claimed before the fields are
// walked: whatever the walk reaches — including t itself — finds it.
func (r *schemaRegistry) define(t reflect.Type, fields map[string]string) string {
	if name, ok := r.names[t]; ok {
		return name
	}
	name := r.nameFor(t)
	def := map[string]any{}
	r.names[t] = name
	r.defs[name] = def
	structSchema(def, t, r, fields)
	return name
}

// nameFor is the name t is described under: its Go name, qualified by the app
// that declared it when it came in on a graft, and qualified by its package
// when a DIFFERENT type already holds the name it would take. Two packages may
// both call a type Config, and a registry that let the second overwrite the
// first would publish one type's fields under the other's name — with both ops'
// $refs pointing at whichever arrived last.
//
// The origin qualification is UNCONDITIONAL, not on collision. Qualifying only
// on collision makes a published type name a function of who else is in the
// room: add a schema to one app and another app's type silently renames, which
// renames a method's argument type in every generated SDK. One rule, one extra
// input — not a second naming scheme.
func (r *schemaRegistry) nameFor(t reflect.Type) string {
	// Qualifiers, outermost first: the app that DECLARED the type — always,
	// when it came in on a graft — then its package, only when a different type
	// already holds the name, then an ordinal, only when even that collides.
	qual := ""
	if r.origin != "" {
		qual = r.origin + "."
	}
	base := qual + t.Name()
	if _, taken := r.defs[base]; !taken {
		return base
	}
	if p := t.PkgPath(); p != "" {
		base = qual + p[strings.LastIndexByte(p, '/')+1:] + "." + t.Name()
	}
	for name, n := base, 2; ; n++ {
		if _, taken := r.defs[name]; !taken {
			return name
		}
		name = base + strconv.Itoa(n)
	}
}

// ref points at a definition. The count is what lets a standalone schema tell a
// definition something still refers to from one only its own root named.
func (r *schemaRegistry) ref(name string) map[string]any {
	r.refs[name]++
	return map[string]any{"$ref": r.prefix + name}
}

// schemaOf builds the JSON Schema for t. A named struct is DEFINED in reg and
// referred to by $ref, so one type is described once however many ops reach it;
// everything else is inlined, having no name to be referred to by. A field's doc
// comment becomes its description, keyed "Type.field" because an In and an Out
// can both have a "limit" and they are not the same thing.
//
// reg may be nil, which describes a struct by its shape without expanding it: a
// caller that only needs to know WHAT a value is — the CLI asking a field's flag
// kind — has nowhere to put a definition and no use for one.
func schemaOf(t reflect.Type, reg *schemaRegistry, fields map[string]string) map[string]any {
	if t == nil {
		return map[string]any{"type": "object"}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// A type that states its own wire form — MarshalJSON — is not described by
	// what it is made of, and this is read FIRST because the rule is about the
	// MARSHALER, not about being a struct. json.RawMessage is the case that
	// mattered: a []byte whose MarshalJSON emits raw JSON. Below the slice rule
	// it was published as an array of integers; under a struct-only check it
	// would still be. A relay whose answer is frequently an array or null was
	// asserted to be an object in openapi.yaml and in every SDK built from it.
	if isMarshaler(t) {
		if t == timeType {
			// The one marshaler whose output shape is DOCUMENTED: RFC 3339,
			// which OpenAPI spells `format: date-time`. Its fields are all
			// unexported, so describing it by them published `{}` properties for
			// every timestamp in the fleet — an object where a string goes.
			return map[string]any{"type": "string", "format": "date-time"}
		}
		// Otherwise: any JSON. Unconstrained is not undocumented — the field's
		// prose still lands on it — and it is the only true thing to say.
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			// encoding/json writes a byte SLICE as a base64 string, not as an
			// array of numbers — and a byte ARRAY as the numbers, which is why
			// this is not both kinds. A named []byte that means something else
			// says so with MarshalJSON, and is caught above.
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{
			"type":  "array",
			"items": schemaOf(t.Elem(), reg, fields),
		}
	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaOf(t.Elem(), reg, fields),
		}
	case reflect.Struct:
		if reg == nil {
			return map[string]any{"type": "object"}
		}
		if t.Name() == "" {
			// Anonymous: nothing to name a definition after — and nothing that
			// can name it, so Go cannot spell a recursive one either.
			out := map[string]any{}
			structSchema(out, t, reg, fields)
			return out
		}
		return reg.ref(reg.define(t, fields))
	}
	return map[string]any{"type": "object"}
}

// structSchema fills in one struct's object schema. It is separate from schemaOf
// because a definition is claimed in the registry before it is filled — that
// claim is the cycle guard, and it needs the map to exist first.
func structSchema(into map[string]any, t reflect.Type, reg *schemaRegistry, fields map[string]string) {
	props := map[string]any{}
	var required []string
	for _, f := range wireFields(t) {
		name := jsonFieldName(f)
		if name == "-" {
			continue // exists, but the body does not carry it.
		}
		fs := schemaOf(f.Type, reg, fields)
		if d := fields[t.Name()+"."+name]; d != "" {
			fs["description"] = d
		}
		props[name] = fs
		if tag := f.Tag.Get("validate"); strings.Contains(tag, "required") {
			required = append(required, name)
		}
	}
	into["type"] = "object"
	into["properties"] = props
	if len(required) > 0 {
		into["required"] = required
	}
}

// rootSchemaOf is one type's schema as a STANDALONE document: the type itself
// inlined — an MCP tool's inputSchema has to BE the object, not a pointer at one
// — with every other named type it reaches carried alongside in $defs, so the
// value is self-contained wherever it is sent. What it describes is the same
// definition the OpenAPI document carries, which is the point: one type, one
// schema, whichever projection you read it from.
func rootSchemaOf(t reflect.Type, fields map[string]string) map[string]any {
	reg := newSchemaRegistry(selfDefs)
	root := schemaOf(t, reg, fields)
	ref, isRef := root["$ref"].(string)
	if !isRef {
		return root // already inline: a scalar, a slice, an anonymous struct
	}
	// The root IS a definition. Inline a copy of it, and leave the entry behind
	// only if something OTHER than the copy still points at it — which is
	// exactly what a self-referential type does.
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

// statusText describes one response. The two defaults keep the words they have
// always had, so a document that never declared a status does not churn; anything
// declared gets the standard reason phrase, lowercased to match them.
func statusText(code int) string {
	switch code {
	case 200:
		return "ok"
	case 204:
		return "no content"
	}
	if s := http.StatusText(code); s != "" {
		return strings.ToLower(s)
	}
	return "ok"
}

// exampleFields splits an op's example object into its fields, so a parameter
// can carry the value the example gave it. A malformed or absent example yields
// nothing rather than failing the document — the schema is still true.
func exampleFields(ex json.RawMessage) map[string]json.RawMessage {
	if len(ex) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(ex, &m) != nil {
		return nil
	}
	return m
}

// docFields is the field map when the generator ran, nil otherwise.
func docFields(has bool, d Doc) map[string]string {
	if !has {
		return nil
	}
	return d.Fields
}

// firstSentence is the summary when none was given explicitly: a doc comment's
// opening sentence is already written to be one, which is why Go documents that
// convention in the first place.
//
// A summary is ONE SENTENCE ON ONE LINE. Neither followed from reading the source
// text verbatim up to ". ":
//
//   - a first sentence that WRAPS in Go source put its line break into the
//     OpenAPI summary, the CLI's one-line listing and the first docstring line of
//     every generated SDK — 215 of 387 published summaries carried one;
//   - a sentence ending at a line break has no ". " to stop at, so the scan ran
//     on and swallowed the paragraphs after it, newlines and all.
//
// So the boundary is a period followed by ANY whitespace (or the end), and the
// result is whitespace-collapsed. The source's line breaks are facts about the
// source; a summary is a fact about the operation.
func firstSentence(s string) string {
	if i := sentenceEnd(s); i >= 0 {
		s = s[:i+1]
	} else if i := strings.IndexByte(s, '\n'); i >= 0 {
		// No sentence at all: the first line, as it has always been.
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}

// sentenceEnd is the index of the period that ends the first sentence, or -1.
// "v1.2" and "hanzo.ai" are not sentence ends, because a sentence end is
// followed by space; "e.g. x" reads as one, which is Go's own convention for
// where a doc comment's summary stops and the reason to write "for example".
func sentenceEnd(s string) int {
	for i := strings.IndexByte(s, '.'); i >= 0; {
		if i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\n' || s[i+1] == '\t' || s[i+1] == '\r' {
			return i
		}
		next := strings.IndexByte(s[i+1:], '.')
		if next < 0 {
			return -1
		}
		i += 1 + next
	}
	return -1
}

// jsonFieldName is the reflect view of the wire-name rule; cmd/zipdoc reads the
// AST view of the same rule, from the same function, so a field's doc lands
// under the name the decoder actually uses.
func jsonFieldName(f reflect.StructField) string {
	return jsontag.Name(f.Name, f.Tag.Get("json"))
}

// urlFieldName is the name a field is carried under in the URL — `url:` when the
// field names one, else the name the body uses. "-" opts out, exactly as it does
// for `json:`.
//
// The two tags are the two halves of a request, and they are separate because a
// route may genuinely mean two different things by one word: PUT
// /v1/workers/scripts/:script takes the worker's NAME in the path and its SOURCE
// in the body, both spelled "script". With one name for both, bindURL bound the
// path last — it is the addressing authority, which is right — and deployed a
// worker whose code was its own name. So the route could not be typed at all.
//
//	Name   string `json:"-"      url:"script"` // the URL's, and only the URL's
//	Script string `json:"script" url:"-"`      // the body's, and only the body's
func urlFieldName(f reflect.StructField) string {
	if tag, ok := f.Tag.Lookup("url"); ok {
		return jsontag.Name(f.Name, tag)
	}
	return jsonFieldName(f)
}

// wireFields is the struct's fields AS THE DECODER SEES THEM: its own, plus the
// promoted fields of every embedded struct, in the order encoding/json resolves
// them. index is the path to the field through those embeddings, so a caller can
// reach the value with reflect.Value.FieldByIndex.
//
// It exists because four things asked "what fields does this type carry on the
// wire" and each answered with its own loop over NumField — which is not that
// question. encoding/json PROMOTES an embedded struct's fields to the outer
// object, and an embedded type is very often unexported (a shared patch body
// reused by two routes), so every one of those loops skipped it on IsExported and
// silently dropped every field it carried: the schema published an object with
// only the outer fields, the CLI offered no flag for them, the document declared
// no request body at all when the outer fields were all path params, and bindURL
// would not bind one from the URL. The wire took them the whole time.
//
// One function, so the promotion rule lives once. It answers STRUCTURE only:
// which fields exist, the outer type's and the promoted ones together. WHICH HALF
// of the request carries a field is a different question, answered by `json:` and
// `url:`, and each caller asks it — because a field can be URL-only
// (`json:"-" url:"script"`) or body-only (`json:"script" url:"-"`), and a filter
// here would silently drop one of the two halves.
//
// An embedded POINTER is skipped rather than followed: the decoder allocates it on
// demand, and a nil one has no fields to promote — a projection must not describe
// what binding cannot reach.
func wireFields(t reflect.Type) []reflect.StructField {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	var out []reflect.StructField
	var walk func(reflect.Type, []int)
	seen := map[reflect.Type]bool{}
	walk = func(t reflect.Type, index []int) {
		if seen[t] {
			return // a struct embedding its own type: promote it once.
		}
		seen[t] = true
		defer delete(seen, t)
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			at := append(append([]int{}, index...), i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				// A tagged embedded struct is a NAMED object, not a promotion —
				// that is encoding/json's rule, so it is this one's too.
				if _, tagged := f.Tag.Lookup("json"); !tagged {
					walk(f.Type, at)
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			f.Index = at
			out = append(out, f)
		}
	}
	walk(t, nil)
	return out
}

// jsonMarshaler and timeType are the two types schemaOf consults about a value's
// wire form: the interface that says "not my fields", and the one implementation
// of it whose output shape is documented.
var (
	jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	timeType      = reflect.TypeOf(time.Time{})
)

// isMarshaler reports whether t writes its own JSON. The pointer is checked too
// because that is where MarshalJSON is usually declared, and encoding/json finds
// it there for any addressable value.
func isMarshaler(t reflect.Type) bool {
	return t.Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(jsonMarshaler)
}

// swaggerHTML is the minimal Swagger UI shell. Loads the UI from a CDN
// and points it at /.well-known/openapi.json. About 30 lines — no bundled
// JS in the binary.
const swaggerHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>API Docs</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({
        url: "/.well-known/openapi.json",
        dom_id: "#swagger-ui",
        deepLinking: true,
      });
    };
  </script>
</body>
</html>
`

// OpenAPISpec returns the OpenAPI 3.1 document for every typed op registered on
// this app — the SAME value served at /.well-known/openapi.json.
//
// It is exported so a service can render its published contract from the routes it
// actually registers, in a build step rather than from a running server. A spec
// generated any other way is a second source of truth, and the whole point of
// deriving it here is that there is only one.
func (a *App) OpenAPISpec() map[string]any { return a.buildOpenAPI() }
