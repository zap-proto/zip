package zip

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
	"github.com/zap-proto/zip/internal/jsontag"
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
	if a.cfg.OpenAPI.Disabled || len(a.ops) == 0 {
		return
	}
	spec := a.buildOpenAPI()
	specJSON, _ := jsonenc.Marshal(spec)

	a.fiber.Get("/.well-known/openapi.json", func(fc fiber.Ctx) error {
		fc.Set("Content-Type", "application/json")
		return fc.Send(specJSON)
	})
	a.fiber.Get("/docs", func(fc fiber.Ctx) error {
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
	schemas := map[string]any{}

	// Sort ops by path,method for deterministic output.
	ops := append([]*registeredOp{}, a.ops...)
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})

	for _, op := range ops {
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
		if hasBody(op.Method) {
			inName := typeName(op.InType)
			if op.InType != nil && inName != "" {
				schemas[inName] = schemaOfDoc(op.InType, schemas, docFields(hasDoc, doc))
				media := map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + inName}}
				// An example is what makes a spec explorable — it is the
				// difference between a reference someone reads and one they can
				// press "try it" on.
				if hasDoc && len(doc.Example) > 0 {
					media["example"] = json.RawMessage(doc.Example)
				}
				opObj["requestBody"] = map[string]any{
					"required": true,
					"content":  map[string]any{"application/json": media},
				}
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
		describe := func(decl map[string]any, field string) map[string]any {
			if help := fields[inName+"."+field]; help != "" {
				decl["description"] = help
			}
			return decl
		}

		params := colonParams(op.Path)
		decls := make([]any, 0, len(params))
		named := make(map[string]bool, len(params))
		for _, p := range params {
			named[strings.ToLower(p)] = true
			decls = append(decls, describe(map[string]any{
				"name": p, "in": "path", "required": true,
				"schema": map[string]any{"type": "string"},
			}, p))
		}
		// Query parameters. A bodyless method binds its input from the URL
		// (typed.go bindURL), so every In field that is NOT already a path
		// segment is reachable as `?field=` — and the document has to say so, or
		// it describes a route nobody can call correctly. Declared only where
		// there is no requestBody, because that is exactly where the binder
		// treats the URL as the whole input.
		if !hasBody(op.Method) {
			for _, f := range queryFields(op.InType) {
				if named[strings.ToLower(f.name)] {
					continue
				}
				decls = append(decls, describe(map[string]any{
					"name": f.name, "in": "query", "required": false,
					"schema": f.schema,
				}, f.name))
			}
		}
		if len(decls) > 0 {
			opObj["parameters"] = decls
		}

		// 200 response.
		outName := typeName(op.OutType)
		if op.OutType != nil && outName != "" {
			schemas[outName] = schemaOfDoc(op.OutType, schemas, docFields(hasDoc, doc))
			respMedia := map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + outName}}
			if hasDoc && len(doc.Response) > 0 {
				respMedia["example"] = json.RawMessage(doc.Response)
			}
			opObj["responses"] = map[string]any{
				"200": map[string]any{
					"description": "ok",
					"content":     map[string]any{"application/json": respMedia},
				},
			}
		} else {
			opObj["responses"] = map[string]any{
				"204": map[string]any{"description": "no content"},
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
			"schemas": schemas,
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

// hasBody reports whether a method carries a JSON request body. It is the ONE
// place that rule lives: the same predicate decides that the input is decoded
// from the body (typed.go) and that the document declares a requestBody rather
// than query parameters. Two copies of it would eventually disagree, and the
// document would describe a request the router cannot parse.
func hasBody(method string) bool {
	switch method {
	case "GET", "HEAD", "DELETE":
		return false
	}
	return true
}

// queryField is one URL-bindable input field: the name a caller writes in the
// query string and the schema of the value.
type queryField struct {
	name   string
	schema map[string]any
}

// queryFields lists the top-level scalar fields of a bodyless op's input — the
// exact set bindURL can fill from the URL. Non-scalars are omitted because the
// binder cannot fill them either, so naming them would promise a parameter that
// silently does nothing.
func queryFields(t reflect.Type) []queryField {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []queryField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := jsonFieldName(f)
		if name == "-" {
			continue
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
			out = append(out, queryField{name: name, schema: schemaOf(ft, nil)})
		}
	}
	return out
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

// schemaOf builds a minimal JSON Schema for t. Handles structs, primitives,
// slices, and pointers. Anonymous types become "object" without a ref.
func schemaOf(t reflect.Type, registry map[string]any) map[string]any {
	return schemaOfDoc(t, registry, nil)
}

// schemaOfDoc is schemaOf with the extraction in hand, so a field's doc comment
// becomes its description. Keyed by "Type.field" because an In and an Out can
// both have a "limit" and they are not the same thing.
func schemaOfDoc(t reflect.Type, registry map[string]any, fields map[string]string) map[string]any {
	if t == nil {
		return map[string]any{"type": "object"}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
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
		return map[string]any{
			"type":  "array",
			"items": schemaOfDoc(t.Elem(), registry, fields),
		}
	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaOfDoc(t.Elem(), registry, fields),
		}
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := jsonFieldName(f)
			if name == "-" {
				continue
			}
			fs := schemaOfDoc(f.Type, registry, fields)
			if fields != nil {
				if d := fields[t.Name()+"."+name]; d != "" {
					fs["description"] = d
				}
			}
			props[name] = fs
			if tag := f.Tag.Get("validate"); strings.Contains(tag, "required") {
				required = append(required, name)
			}
		}
		out := map[string]any{
			"type":       "object",
			"properties": props,
		}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	return map[string]any{"type": "object"}
}

// firstSentence is the summary when none was given explicitly: a doc comment's
// opening sentence is already written to be one, which is why Go documents that
// convention in the first place.
// docFields is the field map when the generator ran, nil otherwise.
func docFields(has bool, d Doc) map[string]string {
	if !has {
		return nil
	}
	return d.Fields
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// jsonFieldName is the reflect view of the wire-name rule; cmd/zipdoc reads the
// AST view of the same rule, from the same function, so a field's doc lands
// under the name the decoder actually uses.
func jsonFieldName(f reflect.StructField) string {
	return jsontag.Name(f.Name, f.Tag.Get("json"))
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
