package zip

// GraphQL execution — the running half of the SIXTH projection.
//
// [App.GraphQLSDL] says what this app can answer; this file answers it. The
// resolver behind every field is op.direct, the same contract the REST route
// runs: validate → authorize → the handler. There is no second way into an op
// and no second set of checks, so a field cannot be reachable here in a way it
// is not reachable there. That is the property this file exists to keep.
//
// # A read is a Query and a write is a Mutation, enforced not just published
//
// The schema puts GET ops under Query and the rest under Mutation. Execution
// applies the SAME split when it resolves a name, so a mutating op is not merely
// documented elsewhere — it is invisible to a query operation and cannot be
// invoked by one. A write reachable as a read is a write behind a cache.
//
// # A header is ambient, never an argument
//
// op.direct takes an In the caller already holds, so unlike the REST seam it
// binds no headers. This file binds them from the request behind the context,
// and the schema does not publish a `header:` field as an argument at all — an
// argument naming one is refused. Both halves of one rule: what the gateway
// sets, a client cannot supply.
//
// # What is not served, by name
//
// Subscriptions, because no op is a stream. Introspection, because the schema is
// published whole at GET [GraphPath], which is what a code generator reads.
// Both are refused in words rather than answered with a confusing nothing.

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// GraphPath is where the graph projection lives: GET returns the schema, POST
// runs a request against it. One path, because the schema and the executor are
// two views of one thing and a client that has the address has both.
const GraphPath = "/.well-known/graph"

// GraphRequest is a GraphQL request as every client sends one.
type GraphRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
	Operation string         `json:"operationName,omitempty"`
}

// GraphResponse is a GraphQL response.
//
// Data is absent when execution never began — a document that would not parse,
// an operation that could not be chosen — and present, though it may hold nulls,
// once it did. That distinction is the whole shape of a GraphQL answer, so it is
// also what the HTTP handler reads to choose a status code.
type GraphResponse struct {
	Data   any          `json:"data,omitempty"`
	Errors []GraphError `json:"errors,omitempty"`
}

// GraphError is one thing that went wrong, and where.
type GraphError struct {
	Message string `json:"message"`
	Path    []any  `json:"path,omitempty"`
}

// GraphQL runs one request against this app's ops.
//
// It takes only a context because everything a call needs is already in one: the
// caller's identity for the authorizer, and the request behind it for any header
// an op declares. That is what lets the same executor serve an HTTP POST, a
// test, or an in-process caller without any of them holding a transport.
func (a *App) GraphQL(ctx context.Context, req GraphRequest) GraphResponse {
	return a.graph(ctx, req, nil)
}

func (a *App) graph(ctx context.Context, req GraphRequest, foreign Resolver) GraphResponse {
	doc, err := parseGraph(req.Query)
	if err != nil {
		return GraphResponse{Errors: []GraphError{{Message: err.Error()}}}
	}
	op, err := doc.pick(req.Operation)
	if err != nil {
		return GraphResponse{Errors: []GraphError{{Message: err.Error()}}}
	}
	vars, err := coerce(op.vars, req.Variables)
	if err != nil {
		return GraphResponse{Errors: []GraphError{{Message: err.Error()}}}
	}

	e := &graph{frags: doc.frags, vars: vars}
	fields := a.graphFields(op.kind)
	data := map[string]any{}
	for _, s := range e.expand(op.sel, nil) {
		if e.skipped(s) {
			continue
		}
		key := s.key()
		at := []any{key}
		switch {
		case s.name == "__typename":
			data[key] = title(op.kind)
		case strings.HasPrefix(s.name, "__"):
			e.fail(at, "introspection is not served here; the schema is published at GET "+GraphPath)
			data[key] = nil
		case foreign != nil:
			data[key] = e.callForeign(ctx, foreign, op.kind, s, at)
		default:
			f, ok := fields[s.name]
			if !ok {
				e.fail(at, fmt.Sprintf("no %s field %q", op.kind, s.name))
				data[key] = nil
				continue
			}
			data[key] = e.call(ctx, f, s, at)
		}
	}
	return GraphResponse{Data: data, Errors: e.errs}
}

// graphFields is the name→op index for ONE operation kind. Building it per kind
// is what enforces the split: a mutating op is simply absent when a query is
// running, so no lookup can reach it.
func (a *App) graphFields(kind string) map[string]*registeredOp {
	out := map[string]*registeredOp{}
	for _, op := range a.Registry() {
		if op.OperationID == "" {
			continue
		}
		if (kind == "query") != (op.Method == http.MethodGet) {
			continue
		}
		out[gqlName(op.OperationID)] = op
	}
	return out
}

// installGraph wires the projection at [GraphPath], where zip's own control
// plane lives.
//
// It follows the OpenAPI document's condition rather than carrying one of its
// own: the projections are one control plane seen six ways, and an app that
// publishes no document should not publish a schema of the same ops either.
func (a *App) installGraph() {
	if a.cfg.OpenAPI.Disabled || len(a.Registry()) == 0 {
		return
	}
	a.MountGraph(GraphPath)
}

// Resolver answers one root field for a graph this app serves but does not own.
//
// kind is "query" or "mutation" — the operation the field was selected under. It
// is passed because only the resolver knows whether a foreign field is safe to
// read: this app has no registry entry to check, so the rule that a write is not
// reachable as a read has to be enforced where the knowledge is.
//
// args arrive with variables already substituted, named as the schema publishes
// them. The value returned is projected through the selection set exactly as a
// local op's result is.
type Resolver func(ctx context.Context, kind, field string, args map[string]any) (any, error)

// MountGraphFor serves a graph whose schema and resolver come from elsewhere.
//
// The parsing, the variables, the fragments, the directives and the projection
// are the same ones a local graph gets — only the answer to "what is this field"
// changes. That is the whole reason this exists: a deployment that answers with
// many processes has one surface and should not grow a second GraphQL
// implementation to describe it.
//
// schema is called per request, so a composed schema stays current.
func (a *App) MountGraphFor(path string, schema func() string, resolve Resolver) {
	a.mountGraph(path, schema, resolve)
}

// MountGraph serves the graph projection at path: GET renders the schema, POST
// runs a request against it.
//
// A host publishes its own address space, so where the projection ANSWERS is the
// host's decision while what it answers is not — the same split that puts zip's
// OpenAPI document at /.well-known and a product's at /v1. One implementation,
// mounted wherever a caller is told to look.
//
// The schema is rendered per request rather than captured here, so a host may
// mount this before it finishes registering: the document always describes the
// registry as it stands when someone asks.
func (a *App) MountGraph(path string) {
	a.mountGraph(path, a.GraphQLSDL, nil)
}

func (a *App) mountGraph(path string, schema func() string, resolve Resolver) {
	a.control(fiber.MethodGet, path, func(fc fiber.Ctx) error {
		fc.Set("Content-Type", "text/plain; charset=utf-8")
		return fc.SendString(schema())
	})
	a.control(fiber.MethodPost, path, func(fc fiber.Ctx) error {
		var req GraphRequest
		resp := GraphResponse{}
		if err := jsonenc.Unmarshal(fc.Body(), &req); err != nil {
			resp.Errors = []GraphError{{Message: "invalid request body: " + err.Error()}}
		} else {
			resp = a.graph(callerContext(fc), req, resolve)
		}
		body, err := jsonenc.Marshal(resp)
		if err != nil {
			return err
		}
		// Nothing in Data means execution never began, so there is no partial
		// answer to be right about — the request itself was refused. Once it began
		// the answer is 200 even where fields failed, which is what a GraphQL
		// client reads its errors from.
		code := http.StatusOK
		if resp.Data == nil {
			code = http.StatusBadRequest
		}
		fc.Set("Content-Type", "application/json")
		return fc.Status(code).Send(body)
	})
}

// graph is one request in flight: the fragments it may spread, the variables it
// was given, and the errors found so far.
type graph struct {
	frags map[string][]*gqlSel
	vars  map[string]any
	errs  []GraphError
}

func (e *graph) fail(path []any, msg string) {
	e.errs = append(e.errs, GraphError{Message: msg, Path: append([]any{}, path...)})
}

// call resolves one field: build the In from the arguments, bind the ambient
// headers, run the op's own contract, and project what comes back.
func (e *graph) call(ctx context.Context, op *registeredOp, s *gqlSel, path []any) any {
	in, err := e.input(op, s.args)
	if err != nil {
		e.fail(path, err.Error())
		return nil
	}
	if op.readsHeaders {
		// AFTER the arguments and from the request itself, exactly as the REST
		// seam binds them: the header is the authority for the field it names.
		bindHeaders(in, headerOf(ctx))
	}
	out, err := op.direct(ctx, in)
	if err != nil {
		e.fail(path, err.Error())
		return nil
	}
	if out == nil {
		// A void op answers that it happened, which is what its Boolean says.
		return true
	}
	raw, err := jsonenc.Marshal(out)
	if err != nil {
		e.fail(path, err.Error())
		return nil
	}
	var node any
	if err := jsonenc.Unmarshal(raw, &node); err != nil {
		e.fail(path, err.Error())
		return nil
	}
	return e.project(node, op.OutType, s.sel, path)
}

// callForeign resolves one field this app does not own.
//
// The resolver receives the operation kind as well as the field, because there is
// no registry entry here to check a write against. Everything after the answer is
// identical to a local field: the value is normalised through JSON and projected
// through the selection set, so a caller cannot tell which side answered.
func (e *graph) callForeign(ctx context.Context, r Resolver, kind string, s *gqlSel, path []any) any {
	args := make(map[string]any, len(s.args))
	for k, v := range s.args {
		rv, err := e.resolve(v)
		if err != nil {
			e.fail(path, err.Error())
			return nil
		}
		args[k] = rv
	}
	out, err := r(ctx, kind, s.name, args)
	if err != nil {
		e.fail(path, err.Error())
		return nil
	}
	if out == nil {
		return nil
	}
	raw, err := jsonenc.Marshal(out)
	if err != nil {
		e.fail(path, err.Error())
		return nil
	}
	var node any
	if err := jsonenc.Unmarshal(raw, &node); err != nil {
		e.fail(path, err.Error())
		return nil
	}
	// No Go type: the resolver answered with a shape only it knows, so the decoded
	// JSON is the whole description and projection reads it directly.
	return e.project(node, nil, s.sel, path)
}

// input builds the op's In from the field's arguments.
//
// Through JSON and the same decoder the REST seam uses, so a value binds here
// exactly as it binds there — one set of decode rules, not two that can drift.
func (e *graph) input(op *registeredOp, args map[string]any) (any, error) {
	if op.InType == nil {
		return nil, fmt.Errorf("op %q takes no input this projection can build", op.OperationID)
	}
	ptr := reflect.New(op.InType)
	if len(args) == 0 {
		return ptr.Interface(), nil
	}
	names := graphArgs(op.InType)
	body := map[string]any{}
	for k, v := range args {
		j, ok := names[k]
		if !ok {
			// Named for the reader: an argument that is not in the schema is
			// either a typo or an attempt to reach a field the schema withholds,
			// and both deserve to be told rather than quietly dropped.
			return nil, fmt.Errorf("unknown argument %q", k)
		}
		rv, err := e.resolve(v)
		if err != nil {
			return nil, err
		}
		body[j] = rv
	}
	raw, err := jsonenc.Marshal(body)
	if err != nil {
		return nil, err
	}
	if err := jsonenc.Unmarshal(raw, ptr.Interface()); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	return ptr.Interface(), nil
}

// graphArgs maps each argument name the schema publishes to the JSON name the
// field carries. Two names because gqlName may have rewritten a character JSON
// allows and GraphQL does not; without the mapping the rewritten ones would
// silently fail to bind.
func graphArgs(t reflect.Type) map[string]string {
	out := map[string]string{}
	for _, f := range wireFields(t) {
		if headerFieldName(f) != "" {
			continue // ambient; see this file's header
		}
		n := jsonFieldName(f)
		if n == "-" {
			continue
		}
		out[gqlName(n)] = n
	}
	return out
}

// resolve replaces variable references with the values the request supplied.
func (e *graph) resolve(v any) (any, error) {
	switch t := v.(type) {
	case gqlVarRef:
		got, ok := e.vars[string(t)]
		if !ok {
			return nil, fmt.Errorf("variable $%s has no value", string(t))
		}
		return got, nil
	case []any:
		out := make([]any, len(t))
		for i, el := range t {
			r, err := e.resolve(el)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, el := range t {
			r, err := e.resolve(el)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	}
	return v, nil
}

// project filters a result down to what was asked for, walking the Go type
// alongside the decoded value.
//
// The type is carried because it answers a question the value cannot: a field
// missing from the JSON because it was empty is a null, while a field missing
// from the TYPE is a mistake worth naming. Without the type those two are the
// same absence and every typo returns null.
func (e *graph) project(node any, t reflect.Type, sel []*gqlSel, path []any) any {
	t = deref(t)
	leaf := t == nil || t.Kind() != reflect.Struct || t == timeType || isMarshaler(t)
	if len(sel) == 0 {
		if !leaf {
			e.fail(path, "field of object type needs a selection of subfields")
			return nil
		}
		return node
	}
	if t != nil && leaf && node != nil {
		if _, obj := node.(map[string]any); !obj {
			e.fail(path, "field of scalar type cannot have a selection of subfields")
			return nil
		}
	}
	switch n := node.(type) {
	case nil:
		return nil
	case []any:
		var et reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			et = t.Elem()
		}
		out := make([]any, len(n))
		for i, el := range n {
			out[i] = e.project(el, et, sel, sub(path, i))
		}
		return out
	case map[string]any:
		return e.object(n, t, sel, path)
	default:
		e.fail(path, "field of scalar type cannot have a selection of subfields")
		return nil
	}
}

func (e *graph) object(n map[string]any, t reflect.Type, sel []*gqlSel, path []any) map[string]any {
	out := map[string]any{}
	for _, s := range e.expand(sel, nil) {
		if e.skipped(s) {
			continue
		}
		key := s.key()
		if s.name == "__typename" {
			out[key] = "JSON"
			if t != nil {
				out[key] = gqlType(t, "")
			}
			continue
		}
		if t == nil {
			// Nothing declares this value's shape, so a field that is absent is
			// absent — not a mistake. With a Go type the two are distinguishable
			// and a missing field IS named; here there is nothing to name it
			// against, and inventing an error would refuse valid answers.
			jk, found := keyOf(n, s.name)
			if !found {
				out[key] = nil
				continue
			}
			out[key] = e.project(n[jk], nil, s.sel, sub(path, key))
			continue
		}
		f, ok := fieldOf(t, s.name)
		if !ok {
			e.fail(sub(path, key), fmt.Sprintf("no field %q on this type", s.name))
			out[key] = nil
			continue
		}
		out[key] = e.project(n[jsonFieldName(f)], f.Type, s.sel, sub(path, key))
	}
	return out
}

// keyOf finds the decoded key a selected name refers to. The schema spells a
// field with gqlName, so the key it came from is the one that spells the same.
func keyOf(n map[string]any, name string) (string, bool) {
	if _, ok := n[name]; ok {
		return name, true
	}
	for k := range n {
		if gqlName(k) == name {
			return k, true
		}
	}
	return "", false
}

// fieldOf finds the struct field a selected name refers to, by the same name the
// schema published it under.
func fieldOf(t reflect.Type, name string) (reflect.StructField, bool) {
	for _, f := range wireFields(t) {
		n := jsonFieldName(f)
		if n == "-" {
			continue
		}
		if gqlName(n) == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// expand flattens fragment spreads into the selection that spread them.
//
// seen is a PATH, not a visited set: a fragment may be spread twice in one
// selection and that is legal, while a fragment that reaches itself is not and
// would not terminate.
func (e *graph) expand(sel []*gqlSel, seen map[string]bool) []*gqlSel {
	var out []*gqlSel
	for _, s := range sel {
		if s.spread == "" {
			out = append(out, s)
			continue
		}
		body, ok := e.frags[s.spread]
		if !ok {
			e.fail(nil, fmt.Sprintf("unknown fragment %q", s.spread))
			continue
		}
		if seen == nil {
			seen = map[string]bool{}
		}
		if seen[s.spread] {
			e.fail(nil, fmt.Sprintf("fragment %q spreads itself", s.spread))
			continue
		}
		seen[s.spread] = true
		out = append(out, e.expand(body, seen)...)
		delete(seen, s.spread)
	}
	return out
}

// skipped applies @skip and @include, the two directives every GraphQL client
// emits for a conditional field.
func (e *graph) skipped(s *gqlSel) bool {
	if v, ok := e.cond(s.skipIf); ok && v {
		return true
	}
	if v, ok := e.cond(s.includeIf); ok && !v {
		return true
	}
	return false
}

func (e *graph) cond(v any) (bool, bool) {
	if v == nil {
		return false, false
	}
	got, err := e.resolve(v)
	if err != nil {
		e.fail(nil, err.Error())
		return false, false
	}
	b, ok := got.(bool)
	return b, ok
}

// coerce settles each declared variable: the value given, else its default, else
// absent — and refuses a required one that ends up with nothing.
func coerce(defs []gqlVar, given map[string]any) (map[string]any, error) {
	out := map[string]any{}
	declared := map[string]bool{}
	for _, d := range defs {
		declared[d.name] = true
		v, ok := given[d.name]
		if !ok || v == nil {
			if d.def != nil {
				out[d.name] = d.def
				continue
			}
			if d.req {
				return nil, fmt.Errorf("variable $%s is required and was not given", d.name)
			}
			continue
		}
		out[d.name] = v
	}
	for k := range given {
		if !declared[k] {
			return nil, fmt.Errorf("variable $%s was given but the operation does not declare it", k)
		}
	}
	return out, nil
}

func sub(path []any, k any) []any {
	out := make([]any, len(path)+1)
	copy(out, path)
	out[len(path)] = k
	return out
}
