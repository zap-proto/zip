package zip

// GraphQL execution — the running half of the SIXTH projection, and the one
// GraphQL engine: a graph this app derives from its own ops and a graph it
// serves for something else ([App.MountGraphFor]) are parsed, gathered,
// executed and written by the code in this file and document.go, and only the
// answer to "what is this field" differs between them.
//
// The resolver behind every LOCAL field is op.direct, the same contract the REST
// route runs: validate → authorize → the handler. There is no second way into an
// op and no second set of checks, so a field cannot be reachable here in a way it
// is not reachable there. That is the property this file exists to keep.
//
// # A read is a Query and a write is a Mutation, enforced not just published
//
// The schema puts GET ops under Query and the rest under Mutation. Execution
// applies the SAME split when it resolves a name, so a mutating op is not merely
// documented elsewhere — it is invisible to a query operation and cannot be
// invoked by one. A write reachable as a read is a write behind a cache. A
// foreign [Resolver] is told the operation kind for the same reason.
//
// # A header is ambient, never an argument
//
// op.direct takes an In the caller already holds, so unlike the REST seam it
// binds no headers. This file binds them from the request behind the context,
// and the schema does not publish a `header:` field as an argument at all — an
// argument naming one is refused. Both halves of one rule: what the gateway
// sets, a client cannot supply.
//
// # Gathered before it runs
//
// A request is read whole before any field resolves: fragments spread, @skip and
// @include decided, variables put in place, and fields that answer under one key
// merged, as the specification's CollectFields does. What comes out is a tree of
// [Selection] values, and it is what a resolver is handed, so a resolver that
// fronts something with its own planner sees everything the caller asked for
// below the field. Every bound — depth, width, size — is checked on that tree,
// so a request that would exceed one is refused before it costs anything.
//
// # What is not served, by name
//
// Subscriptions, and every op that is not one JSON value in and one out (see
// registeredOp.plain): a field resolves to a value, so bytes, a form, a stream,
// an upgrade, a redirect and a union are no field. Introspection, because the
// schema is published whole at GET of the address the graph answers at, which is
// what a code generator reads. Each is refused in words rather than answered
// with a confusing nothing.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// GraphPath is where the graph projection lives: GET returns the schema, POST
// runs a request against it. One path, because the schema and the executor are
// two views of one thing and a client that has the address has both.
const GraphPath = "/.well-known/graph"

// The bounds on one request. Each is checked on the gathered request, before any
// field resolves, and each REFUSES rather than truncates: an answer that quietly
// left out what did not fit would read as complete.
const (
	// rootLimit bounds the fields one operation selects at its root. A root
	// field is an op call or a resolver's hop — for a host fronting many
	// processes, a connection to one of them, which may first have to start —
	// so the width of a query is fan-out, and one caller should not be able to
	// ask for an unbounded amount of it in a single request.
	rootLimit = 64
	// fieldLimit bounds the fields one request selects in all, and then the
	// resolutions it may cost. Fragments make a document's size a poor guide to
	// its expansion — a fragment spread under two keys is two copies, and a chain
	// of them doubles at every link — so the selection is counted as the request
	// is gathered. And data makes the selection a poor guide to the work: a list
	// of objects whose field answers more objects grows by the list's length at
	// every level, from a few dozen bytes of query. So while it runs, every
	// resolver call (a root field, an object's field) and every object reached
	// (an answer, each member of a list) is counted against the same bound, and
	// the request is refused whole once the count passes it.
	fieldLimit = 10000
	// rootParallel bounds how many root fields of a query resolve at once. They
	// are independent, so they overlap; each may be a hop to another process, so
	// a request of rootLimit of them must not be rootLimit connections at once.
	rootParallel = 8
)

// GraphRequest is a GraphQL request as every client sends one.
type GraphRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
	Operation string         `json:"operationName,omitempty"`
}

// GraphResponse is a GraphQL response.
//
// Data is absent when execution never began — a document that would not parse,
// an operation that could not be chosen, a request over a bound — and present,
// though it may hold nulls, once it did. That distinction is the whole shape of
// a GraphQL answer, so it is also what the HTTP handler reads to choose a status
// code.
//
// On the wire an answer's keys come in the order the request selected them, as
// the specification asks; in Go, Data is plain maps and slices.
type GraphResponse struct {
	Data   any          `json:"data,omitempty"`
	Errors []GraphError `json:"errors,omitempty"`

	order []Selection // the gathered request, which orders Data's keys on the wire
}

// GraphError is one thing that went wrong, and where.
type GraphError struct {
	Message string `json:"message"`
	Path    []any  `json:"path,omitempty"`
}

// Selection is one field a request selected, as a resolver receives it:
// fragments spread, @skip and @include applied, variables in its arguments
// replaced by their values, and fields answering under one key merged.
//
// Fields is what the field selects in turn, so a resolver holds the whole of
// what was asked below it: enough to fetch only what is selected, to plan one
// query for a subtree rather than one per field, or to refuse a request too deep
// for what it costs to answer.
type Selection struct {
	// Alias is the key the field answers under when the request renamed it, and
	// empty when it answers under its own name.
	Alias string
	// Name is the field, as the schema publishes it.
	Name string
	// Args are the field's arguments with variables substituted. An argument
	// whose variable was declared and not given is absent, as if never written.
	Args map[string]any
	// Fields is the field's own selection, empty for a leaf.
	Fields []Selection
}

// Key is the name the field answers under: its alias when it has one.
func (s Selection) Key() string {
	if s.Alias != "" {
		return s.Alias
	}
	return s.Name
}

// Depth is how many levels the selection nests, counting itself: a leaf is 1.
func (s Selection) Depth() int {
	d := 0
	for _, f := range s.Fields {
		if n := f.Depth(); n > d {
			d = n
		}
	}
	return d + 1
}

// Bind decodes the field's arguments into v, a pointer to a struct, by each
// field's JSON name, and checks v's `validate` tags.
//
// It is the decode an op's In gets from a body, so an argument binds in a
// resolver exactly as it binds in a handler. An argument v has no field for is
// refused rather than dropped: it is either a typo or an attempt at something
// the schema withholds, and both deserve to be told. Each refusal is a 400 the
// caller reads, and names the argument and the type it takes in the schema's
// words, not the decoder's.
func (s Selection) Bind(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("zip: Bind needs a pointer to a struct, got %T", v)
	}
	fields := map[string]reflect.StructField{}
	for _, f := range wireFields(rv.Elem().Type()) {
		if n := jsonFieldName(f); n != "-" {
			fields[n] = f
		}
	}
	for k, val := range s.Args {
		f, ok := fields[k]
		if !ok {
			return ErrBadRequest(fmt.Sprintf("unknown argument %q on field %q", k, s.Name))
		}
		dst, err := decodeArg(val, f.Type)
		if err != nil {
			return ErrBadRequest(fmt.Sprintf("argument %q of %q %s", k, s.Name, err.Error()))
		}
		rv.Elem().FieldByIndex(f.Index).Set(dst)
	}
	if err := validate(v); err != nil {
		return ErrBadRequest("argument " + argSentence(strings.TrimPrefix(err.Error(), "field "), s.Name))
	}
	return nil
}

// argSentence turns validate's `"key" is required` into `"key" of "entity" is
// required`, so the refusal names the field the argument belongs to.
func argSentence(msg, field string) string {
	if i := strings.Index(msg, `" `); i > 0 && strings.HasPrefix(msg, `"`) {
		return msg[:i+1] + fmt.Sprintf(" of %q", field) + msg[i+1:]
	}
	return msg + fmt.Sprintf(" (on %q)", field)
}

// decodeArg decodes one argument value into a value of type t, through JSON as
// a body is, and says what t takes when the value does not fit.
func decodeArg(val any, t reflect.Type) (reflect.Value, error) {
	dst := reflect.New(t)
	raw, err := jsonenc.Marshal(val)
	if err == nil {
		err = jsonenc.Unmarshal(raw, dst.Interface())
	}
	if err != nil {
		got := string(raw)
		if len(got) > 40 {
			got = got[:40] + "…"
		}
		g := &sdl{types: map[string]string{}, building: map[string]bool{}}
		return reflect.Value{}, fmt.Errorf("takes %s, not %s", g.typeRef(t, "value"), got)
	}
	return dst.Elem(), nil
}

// Object is a value that answers its own fields.
//
// A resolver that returns one — or a slice of them — is asked for each field the
// request selected on it, with that field's own arguments and selection, instead
// of having the field read out of a value it built in advance. So nesting can be
// traversal: an object's field may resolve to more objects, each asked in turn,
// and a field nobody selected is never computed.
//
// Type is the object's type name in the schema, which is what `__typename`
// answers. An error from Field fails that field alone, at its path; the rest of
// the answer stands. What the caller reads of an error is the message of an
// [HTTPError] with a 4xx status; any other error is logged with the request's
// id, and the caller is told only that id.
type Object interface {
	Type() string
	Field(ctx context.Context, s Selection) (any, error)
}

// Resolver answers one root field for a graph this app serves but does not own.
//
// kind is "query" or "mutation" — the operation the field was selected under. It
// is passed because only the resolver knows whether a foreign field is safe to
// read: this app has no registry entry to check, so the rule that a write is not
// reachable as a read has to be enforced where the knowledge is.
//
// The value returned is projected through the field's selection as a local op's
// result is: an [Object] answers each selected field itself, a Go struct (or a
// list of them) is checked against its type as a local op's Out is, and anything
// else is read through JSON and filtered by the selection. An error is told to
// the caller as an [Object]'s is.
type Resolver func(ctx context.Context, kind string, s Selection) (any, error)

// GraphQL runs one request against this app's ops.
//
// It takes only a context because everything a call needs is already in one: the
// caller's identity for the authorizer, and the request behind it for any header
// an op declares. That is what lets the same executor serve an HTTP POST, a
// test, or an in-process caller without any of them holding a transport.
func (a *App) GraphQL(ctx context.Context, req GraphRequest) GraphResponse {
	return a.graph(ctx, GraphPath, CallerOf(ctx).RequestID, req, nil)
}

// graph runs one request. path is where the graph answers, which is where its
// schema is published; rid is the request's correlation id, which a caller is
// given in place of a failure's detail; foreign is the resolver of a graph this
// app does not derive, and nil for its own.
func (a *App) graph(ctx context.Context, path, rid string, req GraphRequest, foreign Resolver) GraphResponse {
	refuse := func(err error) GraphResponse {
		return GraphResponse{Errors: []GraphError{{Message: err.Error()}}}
	}
	doc, err := parseGraph(req.Query)
	if err != nil {
		return refuse(err)
	}
	op, err := doc.pick(req.Operation)
	if err != nil {
		return refuse(err)
	}
	vars, err := coerce(op.vars, req.Variables)
	if err != nil {
		return refuse(err)
	}
	g := &gather{frags: doc.frags, vars: vars, declared: map[string]bool{}}
	for _, v := range op.vars {
		g.declared[v.name] = true
	}
	roots, err := g.collect(op.sel, 1)
	if err != nil {
		return refuse(err)
	}
	if len(roots) > rootLimit {
		return refuse(fmt.Errorf("%d root fields is %d over the %d one request may select",
			len(roots), len(roots)-rootLimit, rootLimit))
	}

	var local map[string]*registeredOp
	if foreign == nil {
		local = a.graphFields(op.kind)
	}
	// The budget is the request's, shared by every field however many run at
	// once, and spending past it cancels the context every resolver was handed,
	// so one that honours its context stops rather than finishing work nobody
	// will read.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &budget{cancel: cancel}
	values := make([]any, len(roots))
	failed := make([][]GraphError, len(roots))
	one := func(i int) {
		e := &graph{path: path, rid: rid, log: a.logger, b: b}
		values[i] = e.root(ctx, op.kind, roots[i], local, foreign)
		failed[i] = e.errs
	}
	// The root fields of a query are independent reads, so they resolve at once
	// — rootParallel at a time — and a request costs its slowest fields rather
	// than the sum of them. A mutation's run one after another, in the order
	// written, because each may depend on what the one before it changed.
	if op.kind == "query" && len(roots) > 1 {
		var wg sync.WaitGroup
		slots := make(chan struct{}, rootParallel)
		for i := range roots {
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer func() { <-slots; wg.Done() }()
				one(i)
			}()
		}
		wg.Wait()
	} else {
		for i := range roots {
			one(i)
		}
	}
	if b.over.Load() {
		// Refused whole: what was resolved before the bound is a fragment of an
		// answer, and a client reading it could not tell what is missing.
		return refuse(fmt.Errorf("the request needs more than %d resolutions; select fewer fields, "+
			"or less below a list", fieldLimit))
	}

	data := make(map[string]any, len(roots))
	var errs []GraphError
	for i, s := range roots {
		data[s.Key()] = values[i]
		errs = append(errs, failed[i]...)
	}
	return GraphResponse{Data: data, Errors: errs, order: roots}
}

// graphFields is the name→op index for ONE operation kind. Building it per kind
// is what enforces the split: a mutating op is simply absent when a query is
// running, so no lookup can reach it.
func (a *App) graphFields(kind string) map[string]*registeredOp {
	out := map[string]*registeredOp{}
	for _, op := range a.Registry() {
		if op.OperationID == "" || !op.plain() {
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

// MountGraphFor serves a graph whose schema and resolver come from elsewhere:
// GET path renders the schema, POST path runs a request against it.
//
// The parsing, the variables, the fragments, the directives, the bounds and the
// projection are the ones a local graph gets — only the answer to "what is this
// field" changes. That is the whole reason this exists: a deployment that
// answers with many processes, or a store whose data is a graph, has one engine
// to serve it with rather than a second GraphQL implementation beside it.
//
// The two routes are the app's own, declared like any route registered at this
// point: the graph is part of what the app serves, so a host routes to them and a
// document describes them. (zip's projection of the app's own ops, [App.MountGraph],
// is control plane instead.) [Group.MountGraphFor] mounts one inside a group, behind
// the group's middleware.
//
// schema is called per request with the caller's context, so a schema may be
// composed as the deployment changes, or differ by who is asking.
func (a *App) MountGraphFor(path string, schema func(ctx context.Context) (string, error), resolve Resolver) {
	a.mountGraphFor(here(1), path, schema, resolve)
}

// MountGraphFor is [App.MountGraphFor] inside the group: the path is under the
// group's prefix and the group's middleware runs in front of both routes.
func (g *Group) MountGraphFor(path string, schema func(ctx context.Context) (string, error), resolve Resolver) *Group {
	g.on.mountGraphFor(here(1), path, schema, resolve)
	return g
}

func (a *App) mountGraphFor(site callsite, path string, schema func(ctx context.Context) (string, error), resolve Resolver) {
	a.raw(site, fiber.MethodGet, path, []Handler{func(c *Ctx) error {
		sdl, err := schema(callerContext(c.fc))
		if err != nil {
			rid := requestID(c.fc)
			a.logger.Error("graph schema would not render", "request_id", rid, "path", c.fc.Route().Path, "error", err)
			return ErrInternal("the schema would not render (request " + rid + ")")
		}
		c.fc.Set("Content-Type", "text/plain; charset=utf-8")
		return c.fc.SendString(sdl)
	}})
	a.raw(site, fiber.MethodPost, path, []Handler{func(c *Ctx) error {
		// The address as served, prefix and all, because it is the one a caller
		// is told the schema is at.
		return a.serveGraph(c.fc, c.fc.Route().Path, resolve)
	}})
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
	a.control(fiber.MethodGet, path, func(fc fiber.Ctx) error {
		fc.Set("Content-Type", "text/plain; charset=utf-8")
		return fc.SendString(a.GraphQLSDL())
	})
	a.control(fiber.MethodPost, path, func(fc fiber.Ctx) error {
		return a.serveGraph(fc, path, nil)
	})
}

// serveGraph answers one POST: the request decoded, run, and written.
func (a *App) serveGraph(fc fiber.Ctx, path string, resolve Resolver) error {
	rid := requestID(fc)
	var req GraphRequest
	resp := GraphResponse{}
	if err := jsonenc.Unmarshal(fc.Body(), &req); err != nil {
		resp.Errors = []GraphError{{Message: "invalid request body: " + err.Error()}}
	} else {
		resp = a.graph(callerContext(fc), path, rid, req, resolve)
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
}

// requestID is the request's correlation id: the one it arrived with, or one
// minted here and set on the request — so every hop a resolver makes carries it
// — and on the answer, so the caller holds the id a failure is reported under.
func requestID(fc fiber.Ctx) string {
	rid := string(fc.Request().Header.Peek(HeaderRequestID))
	if rid == "" {
		var b [16]byte
		_, _ = rand.Read(b[:])
		rid = hex.EncodeToString(b[:])
		fc.Request().Header.Set(HeaderRequestID, rid)
	}
	fc.Set(HeaderRequestID, rid)
	return rid
}

// MarshalJSON writes the answer with each object's keys in the order the request
// selected them.
func (r GraphResponse) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	if r.Data != nil {
		b.WriteString(`"data":`)
		if err := ordered(&b, r.Data, r.order); err != nil {
			return nil, err
		}
	}
	if len(r.Errors) > 0 {
		if r.Data != nil {
			b.WriteByte(',')
		}
		raw, err := jsonenc.Marshal(r.Errors)
		if err != nil {
			return nil, err
		}
		b.WriteString(`"errors":`)
		b.Write(raw)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// ordered writes v, walking sel beside it: an object the selection shaped comes
// out in the selection's order, and anything it did not shape — a leaf, a JSON
// value — as jsonenc writes it.
func ordered(b *bytes.Buffer, v any, sel []Selection) error {
	switch t := v.(type) {
	case map[string]any:
		if len(sel) == 0 {
			break
		}
		b.WriteByte('{')
		n := 0
		for _, s := range sel {
			val, ok := t[s.Key()]
			if !ok {
				continue
			}
			if n > 0 {
				b.WriteByte(',')
			}
			n++
			k, err := jsonenc.Marshal(s.Key())
			if err != nil {
				return err
			}
			b.Write(k)
			b.WriteByte(':')
			if err := ordered(b, val, s.Fields); err != nil {
				return err
			}
		}
		b.WriteByte('}')
		return nil
	case []any:
		b.WriteByte('[')
		for i, el := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := ordered(b, el, sel); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	}
	raw, err := jsonenc.Marshal(v)
	if err != nil {
		return err
	}
	b.Write(raw)
	return nil
}

// gather reads a parsed operation into the tree of Selections that runs.
type gather struct {
	frags    map[string][]*gqlSel
	vars     map[string]any  // the operation's variables, coerced
	declared map[string]bool // every variable the operation declares
	n        int             // fields gathered so far, held to fieldLimit
}

// collect gathers one selection set: spreads and inline fragments flattened,
// skipped fields dropped, arguments settled, and fields that answer under the
// same key merged into one — their selections joined — as the specification's
// CollectFields does.
//
// A fragment is spread at most once per selection set, which is also the
// specification's rule; spread twice it would select the same fields twice.
func (g *gather) collect(sel []*gqlSel, depth int) ([]Selection, error) {
	if depth > depthLimit {
		return nil, fmt.Errorf("selections nest deeper than %d levels", depthLimit)
	}
	type group struct {
		name string
		args map[string]any
		subs []*gqlSel
	}
	var keys []string
	groups := map[string]*group{}
	visited := map[string]bool{}
	var walk func([]*gqlSel) error
	walk = func(sel []*gqlSel) error {
		for _, s := range sel {
			skip, err := g.skipped(s)
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			switch {
			case s.spread != "":
				if visited[s.spread] {
					continue
				}
				visited[s.spread] = true
				if err := walk(g.frags[s.spread]); err != nil {
					return err
				}
			case s.inline:
				if err := walk(s.sel); err != nil {
					return err
				}
			default:
				args, err := g.args(s.args)
				if err != nil {
					return err
				}
				k := s.key()
				if gr, ok := groups[k]; ok {
					if gr.name != s.name || !reflect.DeepEqual(gr.args, args) {
						return fmt.Errorf("%q answers for two different fields; alias one of them", k)
					}
					gr.subs = append(gr.subs, s.sel...)
					continue
				}
				groups[k] = &group{name: s.name, args: args, subs: append([]*gqlSel(nil), s.sel...)}
				keys = append(keys, k)
			}
		}
		return nil
	}
	if err := walk(sel); err != nil {
		return nil, err
	}

	out := make([]Selection, 0, len(keys))
	for _, k := range keys {
		g.n++
		if g.n > fieldLimit {
			return nil, fmt.Errorf("the request selects more than %d fields", fieldLimit)
		}
		gr := groups[k]
		s := Selection{Name: gr.name, Args: gr.args}
		if k != gr.name {
			s.Alias = k
		}
		if len(gr.subs) > 0 {
			sub, err := g.collect(gr.subs, depth+1)
			if err != nil {
				return nil, err
			}
			s.Fields = sub
		}
		out = append(out, s)
	}
	return out, nil
}

// skipped applies @skip and @include, the two directives every GraphQL client
// emits for a conditional field.
func (g *gather) skipped(s *gqlSel) (bool, error) {
	for _, d := range []struct {
		name string
		cond any
		when bool // the value that leaves the field out
	}{{"skip", s.skipIf, true}, {"include", s.includeIf, false}} {
		if d.cond == nil {
			continue
		}
		v, present, err := g.value(d.cond)
		if err != nil {
			return false, err
		}
		b, ok := v.(bool)
		if !present || !ok {
			return false, fmt.Errorf("@%s needs a Boolean `if`", d.name)
		}
		if b == d.when {
			return true, nil
		}
	}
	return false, nil
}

// args settles a field's arguments. One whose variable was declared and not
// given is left out, as the specification says, rather than sent as a null the
// field never asked for.
func (g *gather) args(in map[string]any) (map[string]any, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		val, present, err := g.value(v)
		if err != nil {
			return nil, err
		}
		if present {
			out[k] = val
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// value replaces variable references with the values the request supplied.
// present is false for a declared variable that was given nothing and has no
// default; a reference to a variable the operation never declared is refused.
func (g *gather) value(v any) (val any, present bool, err error) {
	switch t := v.(type) {
	case gqlVarRef:
		if !g.declared[string(t)] {
			return nil, false, fmt.Errorf("variable $%s is not declared by the operation", string(t))
		}
		got, ok := g.vars[string(t)]
		return got, ok, nil
	case []any:
		out := make([]any, len(t))
		for i, el := range t {
			r, _, err := g.value(el)
			if err != nil {
				return nil, false, err
			}
			out[i] = r
		}
		return out, true, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, el := range t {
			r, ok, err := g.value(el)
			if err != nil {
				return nil, false, err
			}
			if ok {
				out[k] = r
			}
		}
		return out, true, nil
	}
	return v, true, nil
}

// graph is one root field in flight: where the graph answers, the request's id
// and log, the budget it shares with the request's other fields, and the errors
// found so far.
type graph struct {
	path string
	rid  string
	log  luxlog.Logger
	b    *budget
	errs []GraphError
}

func (e *graph) fail(path []any, msg string) {
	e.errs = append(e.errs, GraphError{Message: msg, Path: append([]any{}, path...)})
}

// told reports a resolver's or an op's error at path. A refusal with a 4xx
// status was written for the caller and is what the caller reads. Anything else
// — a store's error, a dial, a 5xx, a panic — is this server's own: its detail
// goes to the log under the request's id, and the caller is given the id.
func (e *graph) told(path []any, err error) {
	var he *HTTPError
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
		msg := he.Msg
		if msg == "" {
			msg = http.StatusText(he.Status)
		}
		e.fail(path, msg)
		return
	}
	if e.b.over.Load() {
		// The request is being refused whole; a resolver that stopped because its
		// context was cancelled has nothing worth logging.
		e.fail(path, "the request is past its budget")
		return
	}
	e.log.Error("graph field failed", "request_id", e.rid, "path", fmt.Sprint(path...), "error", err)
	msg := "internal error"
	if e.rid != "" {
		msg += " (request " + e.rid + ")"
	}
	e.fail(path, msg)
}

// budget is what one request may resolve: fieldLimit resolver calls and objects
// reached, counted across all its fields at once.
type budget struct {
	n      atomic.Int64
	over   atomic.Bool
	cancel context.CancelFunc
}

// spend counts one resolution and reports whether the request may go on.
func (b *budget) spend() bool {
	if b.n.Add(1) <= fieldLimit {
		return true
	}
	if b.over.CompareAndSwap(false, true) {
		b.cancel()
	}
	return false
}

// root resolves one root field: the type name, a refusal of introspection, a
// foreign resolver's answer, or a local op's.
func (e *graph) root(ctx context.Context, kind string, s Selection, local map[string]*registeredOp, foreign Resolver) (out any) {
	at := []any{s.Key()}
	// A field runs on its own goroutine when a query has several, and a panic
	// there would take the process down rather than the request; it fails its
	// field like any other error.
	defer func() {
		if r := recover(); r != nil {
			e.told(at, fmt.Errorf("panic: %v", r))
			out = nil
		}
	}()
	switch {
	case s.Name == "__typename":
		return title(kind)
	case strings.HasPrefix(s.Name, "__"):
		e.fail(at, "introspection is not served here; the schema is published at GET "+e.path)
		return nil
	case !e.b.spend():
		return nil
	case foreign != nil:
		v, err := foreign(ctx, kind, s)
		if err != nil {
			e.told(at, err)
			return nil
		}
		return e.value(ctx, v, nil, s.Fields, at)
	}
	op, ok := local[s.Name]
	if !ok {
		e.fail(at, fmt.Sprintf("no %s field %q", kind, s.Name))
		return nil
	}
	return e.call(ctx, op, s, at)
}

// call resolves one local field: build the In from the arguments, bind the
// ambient headers, run the op's own contract, and project what comes back.
func (e *graph) call(ctx context.Context, op *registeredOp, s Selection, path []any) any {
	in, err := e.input(op, s.Args)
	if err != nil {
		e.fail(path, err.Error())
		return nil
	}
	if op.readsHeaders {
		// AFTER the arguments and from the request itself, exactly as the REST
		// seam binds them: the header is the authority for the field it names.
		bindHeaders(in, headerOf(ctx))
	}
	// The op came out of the composed registry, so it knows the address it is
	// served at. Saying so is what lets a path-keyed rule govern a graph field
	// the same way it governs the REST route: without it the contract fell back
	// to the declaration, which for anything declared on a group is the leaf.
	out, err := op.direct(withOp(ctx, servedOp(op)), in)
	if err != nil {
		e.told(path, err)
		return nil
	}
	// A held op has no value, and a graph answer has only two lanes — data and
	// errors — so it is reported beside the field it was selected for. Projecting
	// the [Approval] through the selection instead would describe it against the
	// op's Out type, which is a shape it does not have.
	if a, ok := out.(*Approval); ok {
		e.fail(path, a.Error())
		return nil
	}
	if out == nil {
		// A void op answers that it happened, which is what its Boolean says.
		return true
	}
	return e.value(ctx, out, op.OutType, s.Fields, path)
}

var objectType = reflect.TypeFor[Object]()

// value projects one resolved value through its selection. An [Object] — or a
// list of them — answers each selected field itself, each one counted against
// the request's budget; anything else is normalised through JSON, the shape a
// caller would have read over REST, and filtered by the selection. A foreign
// answer that is a Go struct, or a list of them, is checked against its own
// type as a local op's Out is: a field the type does not have is named, and
// __typename is the type's.
func (e *graph) value(ctx context.Context, v any, t reflect.Type, sel []Selection, path []any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil
	}
	if o, ok := v.(Object); ok {
		if !e.b.spend() {
			return nil
		}
		return e.object(ctx, o, sel, path)
	}
	if k := rv.Kind(); (k == reflect.Slice || k == reflect.Array) &&
		(rv.Type().Elem().Kind() == reflect.Interface || rv.Type().Elem().Implements(objectType)) {
		if k == reflect.Slice && rv.IsNil() {
			return nil
		}
		if len(sel) == 0 && rv.Type().Elem().Implements(objectType) {
			e.fail(path, "field of object type needs a selection of subfields")
			return nil
		}
		out := make([]any, rv.Len())
		for i := range out {
			if e.b.over.Load() {
				return nil
			}
			out[i] = e.value(ctx, rv.Index(i).Interface(), nil, sel, sub(path, i))
		}
		return out
	}
	if t == nil {
		t = typed(rv.Type())
	}
	raw, err := jsonenc.Marshal(v)
	if err != nil {
		e.told(path, err)
		return nil
	}
	var node any
	if err := jsonenc.Unmarshal(raw, &node); err != nil {
		e.told(path, err)
		return nil
	}
	return e.project(node, t, sel, path)
}

// typed is the Go type a foreign answer is checked against: its own, when it is
// a struct or a list of them that writes its JSON from its fields, and nil for
// anything whose shape only the decoded value knows — a map, an any, a
// self-marshalling type.
func typed(t reflect.Type) reflect.Type {
	bottom := deref(t)
	for bottom.Kind() == reflect.Slice || bottom.Kind() == reflect.Array {
		bottom = deref(bottom.Elem())
	}
	if bottom.Kind() != reflect.Struct || bottom == timeType || isMarshaler(bottom) {
		return nil
	}
	return t
}

// object asks an Object for each selected field. A field that fails is null with
// its reason, and the rest of the object still answers. Each call is counted
// against the request's budget, and none is made once the budget is spent.
func (e *graph) object(ctx context.Context, o Object, sel []Selection, path []any) any {
	if len(sel) == 0 {
		e.fail(path, "field of object type needs a selection of subfields")
		return nil
	}
	out := make(map[string]any, len(sel))
	for _, s := range sel {
		key := s.Key()
		at := sub(path, key)
		if s.Name == "__typename" {
			out[key] = o.Type()
			continue
		}
		if !e.b.spend() {
			return nil
		}
		v, err := o.Field(ctx, s)
		if err != nil {
			e.told(at, err)
			out[key] = nil
			continue
		}
		out[key] = e.value(ctx, v, nil, s.Fields, at)
	}
	return out
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
		body[j] = v
	}
	raw, err := jsonenc.Marshal(body)
	if err != nil {
		return nil, err
	}
	if err := jsonenc.Unmarshal(raw, ptr.Interface()); err != nil {
		// Said in the schema's words: which argument, and what it takes.
		for k, v := range args {
			if f, ok := fieldOf(op.InType, k); ok {
				if _, ferr := decodeArg(v, f.Type); ferr != nil {
					return nil, fmt.Errorf("argument %q %s", k, ferr.Error())
				}
			}
		}
		return nil, fmt.Errorf("the arguments do not fit %q", op.OperationID)
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
		if headerFieldName(f) != "" || cookieFieldName(f) != "" {
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

// project filters a result down to what was asked for, walking the Go type
// alongside the decoded value.
//
// The type is carried because it answers a question the value cannot: a field
// missing from the JSON because it was empty is a null, while a field missing
// from the TYPE is a mistake worth naming. Without the type those two are the
// same absence and every typo returns null.
func (e *graph) project(node any, t reflect.Type, sel []Selection, path []any) any {
	t = deref(t)
	var elem reflect.Type
	list := t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && t.Elem().Kind() != reflect.Uint8
	if list {
		elem = t.Elem()
	}
	// A list is projected member by member against its element's type; what the
	// selection may ask of it is a question about the element.
	if n, ok := node.([]any); ok && (list || t == nil) {
		if len(sel) == 0 && elem != nil && !leafType(elem) {
			e.fail(path, "field of object type needs a selection of subfields")
			return nil
		}
		out := make([]any, len(n))
		for i, el := range n {
			out[i] = e.project(el, elem, sel, sub(path, i))
		}
		return out
	}
	if list {
		t = elem // a null list, or a value that is not one: judged as its member
	}
	leaf := t == nil || leafType(t)
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
	case map[string]any:
		return e.fields(n, deref(t), sel, path)
	default:
		e.fail(path, "field of scalar type cannot have a selection of subfields")
		return nil
	}
}

// leafType reports whether a value of type t is answered whole, with nothing
// selected under it: anything but a struct that writes its JSON from its fields,
// and a list of such leaves.
func leafType(t reflect.Type) bool {
	t = deref(t)
	for t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		if t.Elem().Kind() == reflect.Uint8 {
			return true
		}
		t = deref(t.Elem())
	}
	return t.Kind() != reflect.Struct || t == timeType || isMarshaler(t)
}

// fields projects one decoded object through its selection.
func (e *graph) fields(n map[string]any, t reflect.Type, sel []Selection, path []any) map[string]any {
	out := make(map[string]any, len(sel))
	for _, s := range sel {
		key := s.Key()
		if s.Name == "__typename" {
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
			jk, found := keyOf(n, s.Name)
			if !found {
				out[key] = nil
				continue
			}
			out[key] = e.project(n[jk], nil, s.Fields, sub(path, key))
			continue
		}
		f, ok := fieldOf(t, s.Name)
		if !ok {
			e.fail(sub(path, key), fmt.Sprintf("no field %q on this type", s.Name))
			out[key] = nil
			continue
		}
		out[key] = e.project(n[jsonFieldName(f)], f.Type, s.Fields, sub(path, key))
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

// coerce settles each declared variable: the value given, else its default, else
// absent — and refuses a required one that ends up with nothing.
//
// A variable the request gives and the operation does not declare is ignored,
// as the specification's CoerceVariableValues ignores it: nothing in the
// document can refer to it, since a reference to an undeclared variable is
// refused on its own.
func coerce(defs []gqlVar, given map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, d := range defs {
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
	return out, nil
}

func sub(path []any, k any) []any {
	out := make([]any, len(path)+1)
	copy(out, path)
	out[len(path)] = k
	return out
}
