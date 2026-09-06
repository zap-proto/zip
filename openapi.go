package zip

import (
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
	"github.com/zap-proto/zip/internal/jsontag"
	"github.com/zap-proto/zip/manifest"
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
	if a.cfg.OpenAPI.Disabled || len(a.Registry()) == 0 {
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
func (a *App) buildOpenAPI() map[string]any { return openAPI(a.Manifest()) }

// openAPI is the document one manifest describes. It reads nothing but the
// manifest, so the document a Go app publishes and the document written from a
// C++ or Rust app's own source are produced by this one function — which is what
// makes them the same document rather than three programs that agree for now.
func openAPI(m *manifest.App) map[string]any {
	title := m.Title
	if title == "" {
		title = m.Name
	}
	if title == "" {
		title = "zip API"
	}
	version := m.Version
	if version == "" {
		version = "0.0.0"
	}

	paths := map[string]map[string]any{}
	// One registry for the whole document: a type reached by two ops is one
	// definition in components.schemas that both point at.
	reg := newSchemaRegistry(specDefs)

	// The ops arrive sorted by address — see [App.Manifest] — which is the order
	// this document has always been built in.
	for _, op := range m.Ops {
		// Whose types these are. Empty for this app's own ops, so a document
		// with nothing composed into it is byte-identical to one from an app
		// that composes nothing.
		reg.origin = op.Origin

		// The document's spelling of the router's pattern, from [Template], which
		// is the one place that rule lives. This built its own along the way —
		// replace "/:" with "/{", then repair the missing brace by calling
		// Template anyway — and the shortcut answered for two shapes it was never
		// asked about. A path carrying a parameter AND a literal "}" satisfied the
		// repair's guard without being repaired, so it published "{name" unclosed;
		// and a wildcard has no "/:" at all, so "*" reached the document verbatim,
		// which no path template can mean. A caller reading a route table named
		// that segment one thing and this named it another, and the two resolve as
		// different operations rather than one address spelled twice.
		path := Template(op.Path)

		if _, ok := paths[path]; !ok {
			paths[path] = map[string]any{}
		}
		// opID is the ONE place the id rule lives, shared with the MCP tool
		// list, the op-call plane and the Authorizer's Op — so an operation is
		// addressed by the same token whichever projection you came through.
		opObj := map[string]any{
			"operationId": opID(op.ID, op.Method, op.Path),
			"summary":     op.Summary,
		}
		// Prose and examples the front end lifted from the source: Go's doc pass,
		// Rust's macro reading #[doc], C++'s pass reading ///. Absent when nothing
		// read the source, which degrades to the schema-only spec rather than
		// failing — a spec without descriptions is still a usable spec.
		doc := op.Doc
		if doc != nil {
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
		fields := doc.Prose()

		// Request body.
		if hasRequestBody(m, op) {
			media := map[string]any{"schema": schemaOf(m, op.In, reg, fields)}
			// An example is what makes a spec explorable — it is the difference
			// between a reference someone reads and one they can press "try it"
			// on.
			if doc != nil && len(doc.Example) > 0 {
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
		inName := ""
		if op.In != nil {
			inName = op.In.Name
		}
		// A parameter's example is that field's value in the op's OWN example —
		// the one the doc comment already wrote, split across the parameters
		// that carry it. A bodyless op has no requestBody for the example to
		// live in, and an example that only survives for methods with a body is
		// an example missing from every GET and DELETE in the document.
		var example map[string]json.RawMessage
		if doc != nil {
			example = exampleFields(doc.Example)
		}
		// Two keys, because the two lookups are filed under different names. Prose
		// is filed by the DECLARED FIELD, which is what a doc comment documents; an
		// example is a value in the op's own example document, so it is keyed by
		// the name that value carries on the WIRE. They coincide for most
		// parameters and do not for a wildcard, whose wire name is fiber's *N.
		describe := func(decl map[string]any, docField, wire string) map[string]any {
			if help := fields[inName+"."+docField]; help != "" {
				decl["description"] = help
			}
			if v, ok := example[wire]; ok {
				decl["example"] = v
			}
			return decl
		}

		// A parameter's TYPE is the type of the field it binds to, wherever the
		// URL carried it from. bindURL is one binder over one set of fields, so
		// the document reads that one set too — declaring every path param a
		// string while consulting the input for query params described the same
		// value two different ways depending on which half of the URL it rode in.
		url := urlFields(m, op.In)
		params := pathParams(op.Path)
		decls := make([]any, 0, len(params))
		named := make(map[string]bool, len(params))
		for _, p := range params {
			// Both spellings are spoken for, so a field bound under either is not
			// then described a second time as a query parameter.
			named[strings.ToLower(p.Name)] = true
			named[strings.ToLower(p.Key)] = true
			decls = append(decls, describe(map[string]any{
				"name": p.Name, "in": "path", "required": true,
				"schema": url.paramSchema(p.Key),
			}, url.docKey(p.Key), p.Key))
		}
		// Header parameters. A field carrying a header name is a REQUEST FACT
		// the op declared, so the document names it — that is what makes reading
		// a header part of the contract instead of something a middleware does
		// off to the side where no projection can see it. Declared for every
		// method, because a header rides a POST as readily as a GET, and excluded
		// from the query list below so one field is never described twice.
		for _, h := range headerParams(m, op.In) {
			named[strings.ToLower(h.field)] = true
			decls = append(decls, describe(map[string]any{
				"name": h.header, "in": "header", "required": h.required,
				"schema": h.schema,
			}, h.field, h.field))
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
				}, url.docKey(f.name), f.name))
			}
		}
		if len(decls) > 0 {
			opObj["parameters"] = decls
		}

		// The success response, keyed on the status the op DECLARED — the whole
		// reason WithStatus is on the op rather than set per request: a 201 that
		// only reached the wire would leave every generated client expecting a
		// 200 the service never sends.
		if op.Out != nil && op.Out.Name != "" {
			respMedia := map[string]any{"schema": schemaOf(m, op.Out, reg, fields)}
			if doc != nil && len(doc.Response) > 0 {
				respMedia["example"] = json.RawMessage(doc.Response)
			}
			resp := map[string]any{}
			for _, code := range declaredStatuses(op, 200) {
				entry := map[string]any{
					"description": statusText(code),
					"content":     map[string]any{"application/json": respMedia},
				}
				if h := responseHeaderSchemas(op); h != nil {
					entry["headers"] = h
				}
				resp[strconv.Itoa(code)] = entry
			}
			opObj["responses"] = resp
		} else {
			resp := map[string]any{}
			for _, code := range declaredStatuses(op, 204) {
				entry := map[string]any{"description": statusText(code)}
				if h := responseHeaderSchemas(op); h != nil {
					entry["headers"] = h
				}
				resp[strconv.Itoa(code)] = entry
			}
			opObj["responses"] = resp
		}

		// A gated service can hold ANY op, so its document publishes the held body
		// beside every op's success response and a generated client reads the pair
		// rather than mistaking a held op for a finished one.
		//
		// The op is asked, not the app, because they can differ: a composed child
		// answers to whatever included it (see [adopt]), and a MOUNTED op has no
		// local rule at all — it is another service's op, forwarded, so this
		// document must not promise an answer this app would never send. Never
		// over a 202 the op declared itself, either, which is the op's own meaning
		// for that code.
		if op.Held {
			if resp, ok := opObj["responses"].(map[string]any); ok {
				if _, taken := resp["202"]; !taken {
					resp["202"] = map[string]any{
						"description": "held for approval",
						"content": map[string]any{
							"application/json": map[string]any{"schema": schemaOf(zipTypes, approvalType, reg, nil)},
						},
					}
				}
			}
		}

		paths[path][strings.ToLower(op.Method)] = opObj
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       title,
			"description": m.Description,
			"version":     version,
		},
		"paths": paths,
		"components": map[string]any{
			"schemas": reg.defs,
		},
	}
}

// opID is an operation's name: the one it declared, or the rule for deriving one
// from its address. Both the manifest's ops and the registry's read it, so an
// operation carries one token on every surface.
func opID(id, method, path string) string {
	if id != "" {
		return id
	}
	return ID(method, path)
}

// ID is an operation's name, derived from its method and its ABSOLUTE path.
//
// It is THE rule for that derivation and the only one. The OpenAPI operationId,
// the MCP tool name, the key the by-name call plane resolves and the command the
// CLI spells all read this function, so one operation carries one token on every
// surface and an agent reading the document can call what it just read.
//
// '_' is the SEPARATOR — it encodes '/' — so any character that also folded to
// '_' would collide with a path boundary. That is not hypothetical: a router
// serving both /v1/pricing-policy and /v1/pricing/policy, under an earlier
// "everything non-alphanumeric becomes _" rule, collapsed the two onto one id.
// Hyphenated addresses are permanent (…/git-upload-pack, …/documents/delete-batch),
// so '-' and '.' — both legal in an operationId — are PRESERVED rather than
// folded, which keeps such a pair distinct: get_pricing-policy is not
// get_pricing_policy.
//
// A parameter contributes "by_<name>", so /v1/a/{b} and /v1/a/b do not collapse
// either. Every spelling of one parameter reaches the same name: the router's
// (":id", and the constraint and optional marker that are matching business and
// not naming — see [Template]), the document's ("{id}"), and a wildcard, which
// declares no name and takes the positional one the document gives it.
//
// This is derivation, not proof: a literal '_' in a segment can still alias a
// '/' (/v1/a/b_c against /v1/a/b/c). The walk VERIFIES uniqueness across the
// whole program and refuses to publish a duplicate — that check, not this
// encoding, is what makes the ids trustworthy.
//
// THE LEADING v1 IS DROPPED, because it distinguishes nothing. A segment earns
// its place in an id by telling two operations apart, and this one is on every
// address in the program: it is a constant, and a constant repeated 2,000 times
// is noise carried by four surfaces at once — the document, the tool name an
// agent reads on every turn, the by-name key and the CLI's spelling.
//
// Only v1 goes. A later version is the EXCEPTION and keeps its segment, which
// is both how it stays honest and how it stays unique: get_agents is /v1/agents
// and get_v2_agents is /v2/agents, so the two cannot collapse onto one name the
// walk would then refuse. The default is implicit; a departure from it is not.
func ID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	stars := 0
	first := true
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		if first {
			first = false
			if seg == defaultVersion {
				continue // says nothing: every address carries it
			}
		}
		b.WriteByte('_')
		switch {
		case seg == "*" || seg == "+":
			stars++
			seg = "wildcard" + strconv.Itoa(stars)
			b.WriteString("by_")
		case strings.HasPrefix(seg, "{"):
			// The document's spelling of what paramName reads in the router's.
			seg = paramName(":" + strings.Trim(seg, "{}"))
			b.WriteString("by_")
		case strings.HasPrefix(seg, ":"):
			seg = paramName(seg)
			b.WriteString("by_")
		}
		b.WriteString(sanitize(seg))
	}
	return b.String()
}

// defaultVersion is the version every address is expected to carry, and so the
// one [ID] leaves out of a name. It is a value here rather than a rule spread
// across callers: one place decides what "unversioned by default" means.
const defaultVersion = "v1"

// sanitize reduces a path segment to [a-z0-9.-], the characters that are legal
// in an operationId and cannot be confused with the '_' path separator.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
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
func hasRequestBody(m *manifest.App, op manifest.Op) bool {
	if !hasBody(op.Method) || op.In == nil || op.In.Name == "" {
		return false
	}
	if op.In.Kind != manifest.Record {
		return true // the body IS the whole value — a list, a raw message
	}
	named := map[string]bool{}
	for _, p := range pathParams(op.Path) {
		named[strings.ToLower(p.Name)] = true
		named[strings.ToLower(p.Key)] = true
	}
	// Every field the wire carries, which includes those an embedded struct
	// promotes: asking the outer declaration alone declared no body for an input
	// whose own fields were all path params — the phantom body's mirror image.
	for _, f := range m.Fields(op.In) {
		// A field is in the body when the body names it, and is already carried
		// when the URL binds it to a segment the route matched on. The two names
		// answer two questions, so both are asked.
		if f.JSON == "-" {
			continue
		}
		if !named[strings.ToLower(f.URL)] {
			return true
		}
	}
	return false
}

// urlField is one URL-bindable input field: the name a caller writes in the URL,
// the Go field it binds to, the schema of the value, and whether the handler
// refuses to run without it.
type urlField struct {
	name string
	// field is the name the field carries on the WIRE, which is the key zipdoc
	// files its doc comment under. It is not always the URL name — a wildcard
	// binds under fiber's *N — so looking prose up by the URL name finds nothing
	// for exactly the segment that carries the address.
	field    string
	schema   map[string]any
	required bool
}

// urlSchema is the schema of what a URL carries into a field of type t, and nil
// when it carries nothing.
//
// It is [setScalar]'s rule stated for the document, and the ONE place the two
// are held together: the binder fills exactly what this names, so a parameter
// that does nothing is never published and one that works is never left out.
// The same question was answered by a second kind switch here, and the second
// one drifted — an id bound by [setText] was described by neither.
//
// A type that reads itself from text is a STRING here whatever it is made of.
// An id is [32]byte and a time is a struct, and each is one word in the URL and
// the same word in the JSON body beside it; schemaOf describes what a value is
// MADE of, which for these is not what it looks like.
func urlSchema(m *manifest.App, t *manifest.Type) map[string]any {
	if t == nil {
		return nil
	}
	if t.Text {
		return map[string]any{"type": "string"}
	}
	switch t.Kind {
	case manifest.String, manifest.Bool, manifest.Int, manifest.Uint, manifest.Float:
		return schemaOf(m, t, nil, nil)
	case manifest.List:
		// A []byte with no written form of its own is one base64 string, which
		// the binder does not split and cannot read — so nothing is published.
		if t.Elem != nil && t.Elem.Kind == manifest.Uint && t.Elem.Format == "uint8" {
			return nil
		}
		if item := urlSchema(m, t.Elem); item != nil {
			return map[string]any{"type": "array", "items": item}
		}
	}
	return nil
}

// urlFields lists the fields of an op's input a URL fills — the exact set
// bindURL fills, whether the value arrives as a path segment or a query key.
// ONE list serves both because it is ONE binder: a path param and a query param
// are the same kind of value, so the document describes them from the same
// place. What the binder cannot fill is omitted, because naming it would
// promise a parameter that silently does nothing.
func urlFields(m *manifest.App, t *manifest.Type) urlFieldList {
	var out urlFieldList
	collectURLFields(m, t, "", map[string]bool{}, &out)
	return out
}

// urlRecord reports whether a URL names t's LEAVES through it rather than carrying
// t itself. A struct is that, unless it has a written form of its own — a time
// and an id are one word in a URL, not a pair of braces — in which case
// [urlSchema] already describes it and there is nothing to reach inside.
func urlRecord(t *manifest.Type) bool {
	return t != nil && t.Kind == manifest.Record && !t.Text && !t.States
}

// collectURLFields appends t's URL-borne leaves, naming a record's own leaves
// through it. inside is the record already being walked, so a self-referential
// input names its leaves once rather than forever: the binder stops at the keys
// a caller actually wrote, and a document walk has no keys to stop at.
func collectURLFields(m *manifest.App, t *manifest.Type, prefix string, inside map[string]bool, out *urlFieldList) {
	if t == nil || t.Kind != manifest.Record || inside[t.Ref] {
		return
	}
	inside[t.Ref] = true
	defer delete(inside, t.Ref)
	for _, f := range m.Fields(t) {
		name := f.URL
		if name == "-" {
			continue // on the wire, but not in the URL.
		}
		name = prefix + name
		if urlRecord(&f.Type) {
			collectURLFields(m, &f.Type, name+".", inside, out)
			continue
		}
		schema := urlSchema(m, &f.Type)
		if schema == nil {
			continue // on the wire, but not something a URL carries.
		}
		*out = append(*out, urlField{
			name:   name,
			field:  f.JSON,
			schema: schema,
			// The same requirement that makes a body field required in its schema
			// makes a URL-borne one required in its parameter. The handler refuses
			// the request either way; a document that called it optional was
			// describing a call that cannot succeed, and every generated client
			// made the argument optional to match.
			required: f.Required,
		})
	}
}

type urlFieldList []urlField

// docKey is the wire name of the field bound under the given URL name, which is
// the key zipdoc files its doc comment under. The two differ only where a `url:`
// tag renames the binding — a wildcard's *N being the case that matters. Falls
// back to the URL name, which is right whenever they coincide and is all there is
// to try for a field that carries no wire name at all.
func (l urlFieldList) docKey(name string) string {
	for _, f := range l {
		if strings.EqualFold(f.name, name) && f.field != "" && f.field != "-" {
			return f.field
		}
	}
	return name
}

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

// pathParam is one segment the ROUTE matches on, under both the names it goes by.
//
// The two differ for a wildcard and only for a wildcard: the document calls the
// segment wildcardN, because that is what [Template] writes into the path and a
// template variable must be declared under the name the path uses; the router
// calls it *N, which is fiber's own capture key and therefore what an input's
// `url:` tag has to say to bind it. Carrying both together is what stops one being
// mistaken for the other — declared under Key, the document would name a parameter
// no path contains; bound under Name, the input would bind nothing.
type pathParam struct {
	// Name is the document's spelling, and the name the parameter is declared by.
	Name string
	// Key is the router's spelling, and what `url:` binds against.
	Key string
	// At is which segment of the pattern this is, so a caller substituting a
	// value into the address replaces the segment it found rather than searching
	// for a spelling — the only one that works for a wildcard, whose segment is
	// "*" and matches no name.
	At int
}

// pathParams lists the segments a fiber route pattern matches on, in order —
// every `:name` and every wildcard.
//
// The two names a wildcard goes by are counted on DIFFERENT sequences, which is
// the whole reason both are carried. The document's name is positional, exactly as
// [Template] numbers it, so a declaration made here lands on the brace written
// there. The router's key is fiber's own, and fiber counts each marker separately
// and keeps the marker in the key — measured: `/p/+/q/*` matches under `+1` and
// `*1`, and `/p/+/q/+` under `+1` and `+2`. One counter for both would name a `+`
// route's capture `*1`, which binds nothing and suppresses no query parameter.
//
// It read `:name` alone until a wildcard could be a typed op: after that the path
// carried a {wildcardN} nothing declared, and the value the address IS went
// undeclared or arrived as a query parameter.
func pathParams(path string) []pathParam {
	var out []pathParam
	seen := map[byte]int{}
	all := 0
	for i, seg := range strings.Split(path, "/") {
		switch {
		case seg == "*" || seg == "+":
			all++
			seen[seg[0]]++
			out = append(out, pathParam{
				Name: "wildcard" + strconv.Itoa(all),
				Key:  seg + strconv.Itoa(seen[seg[0]]),
				At:   i,
			})
		case len(seg) > 1 && seg[0] == ':':
			name := paramName(seg)
			out = append(out, pathParam{Name: name, Key: name, At: i})
		}
	}
	return out
}

// typeName is the name a type was declared under, which is usually its Go name.
//
// A type built at run time has no Go name — reflect makes no named types — so a
// struct that [ReadZAP] derived from a .zap declaration carries the name it was
// declared under on its fields instead, and this is where that is read. Every
// projection asks this rather than reflect, so a schema-derived type is named in
// the SDK, the document, the tool list and the schema written back out by the
// name its author gave it, and not by the Go spelling of its shape.
func typeName(t reflect.Type) string {
	if t == nil {
		return ""
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n := t.Name(); n != "" {
		return n
	}
	if t.Kind() == reflect.Struct && t.NumField() > 0 {
		return t.Field(0).Tag.Get("decl")
	}
	return ""
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
	defs   map[string]any    // name → definition
	names  map[string]string // the manifest's struct key → the name it is defined under
	refs   map[string]int    // name → how many $refs point at it

	// origin is the app that DECLARED the op currently being described, when
	// that op arrived through composition — empty for the host's own. It
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
		names:  map[string]string{},
		refs:   map[string]int{},
	}
}

// define describes t in the registry, if it is not there already, and returns
// the name it is described under. The entry is claimed before the fields are
// walked: whatever the walk reaches — including t itself — finds it.
func (r *schemaRegistry) define(m *manifest.App, t *manifest.Type, fields map[string]string) string {
	if name, ok := r.names[t.Ref]; ok {
		return name
	}
	name := r.nameFor(t)
	def := map[string]any{}
	r.names[t.Ref] = name
	r.defs[name] = def
	structSchema(m, def, t, r, fields)
	return name
}

// nameFor is the name t is described under: its Go name, qualified by the app
// that declared it when it came in through composition, and qualified by its package
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
func (r *schemaRegistry) nameFor(t *manifest.Type) string {
	// Qualifiers, outermost first: the app that DECLARED the type — always,
	// when it came in through composition — then its package, only when a different type
	// already holds the name, then an ordinal, only when even that collides.
	qual := ""
	if r.origin != "" {
		qual = r.origin + "."
	}
	base := qual + t.Name
	if _, taken := r.defs[base]; !taken {
		return base
	}
	if p := t.Pkg; p != "" {
		base = qual + p[strings.LastIndexByte(p, '/')+1:] + "." + t.Name
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
func schemaOf(m *manifest.App, t *manifest.Type, reg *schemaRegistry, fields map[string]string) map[string]any {
	if t == nil {
		return map[string]any{"type": "object"}
	}
	// A type that states its own wire form is not described by what it is made
	// of, and this is read FIRST because the rule is about the STATEMENT, not
	// about being a struct. json.RawMessage is the case that mattered: a []byte
	// whose JSON is raw JSON. Below the slice rule it was published as an array
	// of integers; under a struct-only check it would still be. A relay whose
	// answer is frequently an array or null was asserted to be an object in
	// openapi.yaml and in every SDK built from it.
	if t.States {
		if len(t.Schema) > 0 {
			return maps.Clone(t.Schema)
		}
		// Any JSON. Unconstrained is not undocumented — the field's prose still
		// lands on it — and it is the only true thing to say.
		return map[string]any{}
	}
	switch t.Kind {
	case manifest.String:
		return map[string]any{"type": "string"}
	case manifest.Bool:
		return map[string]any{"type": "boolean"}
	case manifest.Int, manifest.Uint:
		return map[string]any{"type": "integer", "format": t.Format}
	case manifest.Float:
		return map[string]any{"type": "number", "format": t.Format}
	case manifest.List, manifest.Fixed:
		if t.Kind == manifest.List && t.Elem != nil && t.Elem.Kind == manifest.Uint && t.Elem.Format == "uint8" {
			// A byte SLICE is written as a base64 string, not as an array of
			// numbers — and a byte ARRAY as the numbers, which is why this is not
			// both kinds. A named []byte that means something else states its own
			// wire form, and is caught above.
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{
			"type":  "array",
			"items": schemaOf(m, t.Elem, reg, fields),
		}
	case manifest.Table:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaOf(m, t.Elem, reg, fields),
		}
	case manifest.Record:
		if reg == nil {
			return map[string]any{"type": "object"}
		}
		if t.Name == "" {
			// Anonymous: nothing to name a definition after — and nothing that
			// can name it, so no declaration can spell a recursive one either.
			out := map[string]any{}
			structSchema(m, out, t, reg, fields)
			return out
		}
		return reg.ref(reg.define(m, t, fields))
	}
	return map[string]any{"type": "object"}
}

// numberFormat names a number's WIDTH and SIGN, which "integer" alone does not.
//
// It matters because the document is what a client is generated from, and one
// of those clients speaks ZAP, where a field IS an offset and a width. A
// generated type that reads uint32 as int64 does not merely mis-print a value:
// it takes eight bytes where the service laid four, and every field after it is
// read from the wrong place. JSON survives the ambiguity because a number is
// self-delimiting; a fixed layout does not.
//
// The names are OpenAPI's own for the two it registers (int32, int64) and the
// Go spelling for the rest, which is what every generator already reads. A
// format is an annotation, so a consumer that ignores it sees exactly the
// document it saw before.
func numberFormat(k reflect.Kind) string {
	switch k {
	case reflect.Int, reflect.Int64:
		return "int64"
	case reflect.Int8:
		return "int8"
	case reflect.Int16:
		return "int16"
	case reflect.Int32:
		return "int32"
	case reflect.Uint, reflect.Uint64:
		return "uint64"
	case reflect.Uint8:
		return "uint8"
	case reflect.Uint16:
		return "uint16"
	case reflect.Uint32:
		return "uint32"
	case reflect.Float32:
		return "float"
	}
	return "double"
}

// structSchema fills in one struct's object schema. It is separate from schemaOf
// because a definition is claimed in the registry before it is filled — that
// claim is the cycle guard, and it needs the map to exist first.
func structSchema(m *manifest.App, into map[string]any, t *manifest.Type, reg *schemaRegistry, fields map[string]string) {
	props := map[string]any{}
	var required []string
	for _, f := range m.Fields(t) {
		if f.JSON == "-" {
			continue // exists, but the body does not carry it.
		}
		fs := schemaOf(m, &f.Type, reg, fields)
		if d := fields[t.Name+"."+f.JSON]; d != "" {
			fs["description"] = d
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

// rootSchemaOf is one type's schema as a STANDALONE document: the type itself
// inlined — an MCP tool's inputSchema has to BE the object, not a pointer at one
// — with every other named type it reaches carried alongside in $defs, so the
// value is self-contained wherever it is sent. What it describes is the same
// definition the OpenAPI document carries, which is the point: one type, one
// schema, whichever projection you read it from.
func rootSchemaOf(m *manifest.App, t *manifest.Type, fields map[string]string) map[string]any {
	reg := newSchemaRegistry(selfDefs)
	root := schemaOf(m, t, reg, fields)
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
//
// headerFieldName is the header a field reads, or "" when it reads none.
//
// A `header:"X-Tenant"` tag is what makes a REQUEST FACT part of the op's
// contract instead of something a middleware smuggles through a context slot.
// Declared here, it appears as a header parameter in the document, a flag on the
// command, and a property in the MCP tool schema — which is the whole test a
// replacement for that middleware has to pass: a fact no projection can see is
// not a fact the API has.
func headerFieldName(f reflect.StructField) string {
	if tag, ok := f.Tag.Lookup("header"); ok {
		return jsontag.Name(f.Name, tag)
	}
	return ""
}

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

// SchemaDescriber is implemented by a type that writes its own JSON and can say
// what that JSON looks like. Without it a marshaler is published as `{}`: true,
// because the fields are not the wire form, but useless to anything generating
// a client — every such field arrives as an untyped value.
//
// The shape is a JSON Schema object, the same vocabulary the rest of the
// document speaks. A numeric carried as a quoted decimal, which is the usual
// reason a type marshals itself, says so directly:
//
//	func (Uint64) JSONSchema() map[string]any {
//	    return map[string]any{"type": "string", "pattern": "^[0-9]+$"}
//	}
//
// Implement it on the value, not the pointer, so a field of either form is
// described. Returning nil means "no shape to state" and falls back to `{}`.
type SchemaDescriber interface {
	JSONSchema() map[string]any
}

var schemaDescriber = reflect.TypeOf((*SchemaDescriber)(nil)).Elem()

// declaredSchema asks a type for its own wire shape, or nil if it does not
// state one. A fresh copy is returned so a caller cannot edit the type's
// answer for everybody else — the schemas are assembled into one document by
// mutation, and a shared map would leak one field's constraints into another.
func declaredSchema(t reflect.Type) map[string]any {
	var d SchemaDescriber
	switch {
	case t.Implements(schemaDescriber):
		d, _ = reflect.Zero(t).Interface().(SchemaDescriber)
	case reflect.PointerTo(t).Implements(schemaDescriber):
		d, _ = reflect.New(t).Interface().(SchemaDescriber)
	default:
		return nil
	}
	if d == nil {
		return nil
	}
	got := d.JSONSchema()
	if got == nil {
		return nil
	}
	out := make(map[string]any, len(got))
	for k, v := range got {
		out[k] = v
	}
	return out
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
func (a *App) OpenAPISpec() map[string]any { return Document(a.Manifest()) }

// primaryStatus is the code an op answers with when its output states nothing —
// the first it declared. The document keys the success response on it, and every
// OTHER declared status gets its own entry beside it (see multiStatus), because
// a code the service can send and the document omits is a code no generated
// client will handle.
// declaredStatuses is every success code an op may answer with — all of them,
// not just the first. An op that declares 200 and 201 can send either, so the
// document says both; publishing one would leave a generated client with no
// branch for the other, which is precisely the hole a per-request status slot
// left open.
func declaredStatuses(op manifest.Op, dflt int) []int {
	if len(op.Statuses) == 0 {
		return []int{dflt}
	}
	return op.Statuses
}
func primaryStatus(op manifest.Op) int {
	if len(op.Statuses) == 0 {
		return 0
	}
	return op.Statuses[0]
}

// headerField is one declared header parameter: the header it reads, the field
// it lands on, and the shape it carries.
type headerField struct {
	header   string
	field    string
	required bool
	schema   map[string]any
}

// headerFields is every `header:`-tagged field of an input type. It walks the
// same wireFields the decoder does, so a promoted field of an embedded struct
// declares its header exactly as an own field would — the document and the
// binder cannot disagree about which fields exist.
func headerParams(m *manifest.App, t *manifest.Type) []headerField {
	var out []headerField
	for _, f := range m.Fields(t) {
		if f.Header == "" {
			continue
		}
		out = append(out, headerField{
			header:   f.Header,
			field:    f.JSON,
			required: f.Required,
			schema:   schemaOf(m, &f.Type, nil, nil),
		})
	}
	return out
}

// responseHeaderSchemas is the `headers` object of a response, from what the op
// declared with [WithResponseHeader].
//
// A response header a caller relies on — a cache directive, a payment challenge,
// a Set-Cookie — is part of the contract, so the document names it. Without this
// the header would be set on the wire and described nowhere, which is the same
// invisibility that made a context slot the wrong home for it.
func responseHeaderSchemas(op manifest.Op) map[string]any {
	if len(op.Headers) == 0 {
		return nil
	}
	out := make(map[string]any, len(op.Headers))
	for _, name := range op.Headers {
		out[name] = map[string]any{
			"description": "Set by " + op.Method + " " + op.Path + ".",
			"schema":      map[string]any{"type": "string"},
		}
	}
	return out
}
