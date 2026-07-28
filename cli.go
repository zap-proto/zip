package zip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/valyala/fasthttp"
)

// CLI — the FOURTH projection. The same typed-op registry (a.ops) that produces
// the REST routes, the OpenAPI document and the MCP tools produces a command
// line: an op becomes `<service> <operation>`, its In fields become flags, its
// doc comment becomes the help, and its Example becomes the example invocation.
//
// A command carries the op's OWN identity, not a second one: Command.OperationID
// is the token the document, the tool list and zip.Call all address it by, and
// an explicit WithOperationID renames the command with them.
//
// Nothing about a command is written anywhere. Registering a typed route adds
// the command, its flags, its help and its example — with no edit to any CLI
// source, which is the only test that proves a CLI is derived rather than
// merely generated once and maintained by hand afterwards. A command therefore
// cannot drift from the API: there is no second place for it to drift in.
//
// Two axes, kept apart:
//
//   - DERIVATION. App.Commands reads the registry in THIS process.
//     CommandsFromSpec reads it over the wire, off the OpenAPI document the
//     owning service derives from that same registry. Same registry, one hop.
//   - EXECUTION. LocalInvoke runs the handler here; Remote.Invoke sends it to a
//     running service.
//
// They compose freely, which is what makes one command tree serve both a fused
// binary and a control-plane client. A client CLI does not link the service
// graph at all: it asks the service what it can do, and a route added this
// morning is a command this afternoon with nothing rebuilt.
//
// It also pairs with Plugin.Lazy: a lazily loaded service starts on the first
// request that reaches its prefix, so running one command starts exactly the
// one service that command touches, and a host composing dozens of them pays
// for the one you used.

// Command is one operation as a command line: the value both the help text and
// the argument parser read. It is the CLI's whole surface — a derivation fills
// it in, the runner consumes it, and neither knows how the other got there.
type Command struct {
	// Service is the first non-version segment of the path ("billing"), and
	// Name is the operation token under it ("invoices-list").
	Service string
	Name    string

	// OperationID is the op's identity — the SAME token the OpenAPI document's
	// operationId, the MCP tool's name and zip.Call all address it by. A command
	// is a projection of an op, not a second thing with a second name, and this
	// is what says WHICH op it projects.
	OperationID string

	// Summary is the one-line help; Description is the full prose. Both come
	// from the handler's doc comment when cmd/zipdoc has run.
	Summary     string
	Description string

	// Method and Path are the operation's identity — the route pattern in
	// fiber's ":name" form, whatever the derivation read it from.
	Method string
	Path   string

	// Args are the path parameters, in path order, as positional arguments. The
	// URL is the addressing authority (see bindPath), so what addresses the
	// resource is positional and what modifies the request is a flag.
	Args []Arg

	// Flags are the remaining In fields.
	Flags []Flag

	// Example is the request body from the doc comment, rendered by the help as
	// a runnable command line.
	Example json.RawMessage

	// op is set only by App.Commands: it is what LocalInvoke runs. A
	// spec-derived command has none and must be executed remotely.
	op *registeredOp
}

// Arg is one path parameter as a positional argument.
type Arg struct {
	Name string // the param name, "app"
	Help string
}

// Flag is one In field as a flag.
type Flag struct {
	Name     string // the flag, kebab-cased and without the dashes: "organization-id"
	Field    string // the JSON field it sets: "organizationId"
	Type     string // string | integer | number | boolean | json
	Help     string
	Required bool
}

// Commands projects every registered typed op into a command. This is the whole
// derivation: no registration, no list of commands, no per-endpoint code.
func (a *App) Commands() []Command {
	cmds := make([]Command, 0, len(a.ops))
	for _, op := range a.ops {
		doc, has := docFor(op.Method, op.Path)
		c := newCommand(op.Method, op.Path, opName(op), op.Summary, doc, has)
		c.op = op
		c.Args, c.Flags = bindIn(op.InType, colonParams(op.Path), docFields(has, doc))
		cmds = append(cmds, c)
	}
	sortCommands(cmds)
	return cmds
}

// newCommand fills in everything that comes from the op's identity and its doc,
// which is the half both derivations share.
func newCommand(method, path, id, summary string, doc Doc, has bool) Command {
	svc, name := commandName(method, path, id)
	c := Command{Service: svc, Name: name, OperationID: id, Summary: summary, Method: method, Path: path}
	if has {
		c.Description = doc.Description
		c.Example = doc.Example
		if c.Summary == "" {
			c.Summary = firstSentence(doc.Description)
		}
	}
	return c
}

// bindIn splits an In type into positional args and flags. A field whose name
// matches a path parameter is positional and is NOT also a flag: the URL
// addresses the resource, so offering a second way to set the same value would
// be two ways to say one thing (and bindPath would overrule one of them).
func bindIn(in reflect.Type, params []string, fieldDocs map[string]string) ([]Arg, []Flag) {
	args := make([]Arg, 0, len(params))
	for _, p := range params {
		args = append(args, Arg{Name: p, Help: fieldDocs[typeName(in)+"."+p]})
	}
	if in == nil {
		return args, nil
	}
	t := in
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return args, nil
	}
	var flags []Flag
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := jsonFieldName(f)
		if name == "-" || isParam(params, name) {
			continue
		}
		flags = append(flags, Flag{
			Name:     kebab(name),
			Field:    name,
			Type:     flagType(f.Type),
			Help:     fieldDocs[t.Name()+"."+name],
			Required: strings.Contains(f.Tag.Get("validate"), "required"),
		})
	}
	// The args' help lives under the field's own json name, which is what the
	// extraction keyed it by; look it up now that the type is in hand.
	for i, a := range args {
		if a.Help == "" {
			args[i].Help = fieldDocs[t.Name()+"."+a.Name]
		}
	}
	return args, flags
}

func isParam(params []string, name string) bool {
	for _, p := range params {
		if strings.EqualFold(p, name) {
			return true
		}
	}
	return false
}

// flagType is a flag's value kind, read off the ONE schema derivation: what a
// field IS on the wire is what schemaOf says it is, and specType is the SAME
// schema-type → flag-kind rule the spec-derived tree uses. A flag spelled from a
// Go type and the same flag spelled from that type's published schema are then
// equal by construction, rather than by two lists of kinds someone has to keep
// in step.
//
// Anything compound lands on JSON: a struct, map or slice has no unambiguous
// flat spelling, and inventing one would be a second encoding of a value JSON
// already encodes. No registry is passed because none is needed — the kind of a
// struct is "object" whatever its fields are, and expanding them here would
// build a definition with nowhere to live.
func flagType(t reflect.Type) string {
	kind, _ := schemaOf(t, nil, nil)["type"].(string)
	return specType(kind)
}

// commandName derives `<service> <operation>` from an op's identity.
//
// The SERVICE is where the command lives in the tree: the first non-version
// static segment of the path, which is the same thing the router groups on.
//
// The OPERATION is the op's own name. An explicit id (WithOperationID) is that
// name — renaming an op has to rename it in EVERY projection or the command line
// addresses something the document has never heard of. Absent one, id is the
// method+path default and the words below spell it out long-hand:
//
//	GET    /v1/paas/apps              → paas    apps-list
//	GET    /v1/paas/apps/:app         → paas    apps-get <app>
//	POST   /v1/paas/apps/:app/deploy  → paas    apps-deploy <app>
//	GET    /v1/paas/apps/:app/deploy  → paas    apps-deploy-get <app>
//	POST   /v1/billing/charge         → billing charge-create
//	DELETE /v1/keys/:id               → keys    delete <id>
//
// The name is a function of that one op and nothing else. It cannot change
// because a sibling route was added — a rule that shortened names while they
// stayed unique would rename a command, and therefore break a script, every
// time an unrelated endpoint shipped.
//
// POST is the only method whose verb an action segment replaces, because POST
// on a path ending in a verb IS the REST spelling of "run this" — `apps-deploy`
// rather than `apps-deploy-create`. Every other method still appends its own
// verb, so no two ops on one path can collide.
func commandName(method, path, id string) (service, name string) {
	// An id that IS the default says nothing the path does not; only an
	// explicit one renames the command.
	if id == defaultOpID(method, path) {
		id = ""
	}
	segs := make([]string, 0, 8)
	for _, s := range strings.Split(path, "/") {
		if s != "" && !isVersion(s) {
			segs = append(segs, s)
		}
	}
	// The service is the first static segment; a leading parameter (rare) is
	// addressing, not naming, and is picked up as an argument like any other.
	svc := 0
	for svc < len(segs) && strings.HasPrefix(segs[svc], ":") {
		svc++
	}
	if svc == len(segs) {
		return "root", opWord("root", id, methodVerb(method, len(segs) > 0))
	}
	service = segs[svc]
	rest := segs[svc+1:]

	first, last := -1, -1
	for i, s := range rest {
		if strings.HasPrefix(s, ":") {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	var words []string
	var tail []string
	if first < 0 {
		words = append(words, rest...)
	} else {
		words = append(words, rest[:first]...)
		tail = rest[last+1:]
		words = append(words, tail...)
	}
	if !(method == "POST" && len(tail) > 0) {
		// One thing or many. A path ending in a parameter is always one. Past
		// that the last word decides, because a route that continues after its
		// parameter has already said which it is: /apps/:app/deploy addresses one
		// deploy, /nodes/:node/jobs addresses many jobs.
		item := last >= 0 && len(tail) == 0
		if !item && len(words) > 0 {
			item = !plural(words[len(words)-1])
		}
		words = append(words, methodVerb(method, item))
	}
	for i, w := range words {
		words[i] = kebab(w)
	}
	service = kebab(service)
	return service, opWord(service, id, strings.Join(words, "-"))
}

// opWord is the operation token: the explicit id when there is one, else the
// name spelled out of the route. An explicit id is kebab-cased like any other
// identifier and loses a leading service word it would otherwise repeat —
// `billing_charge_create` under `billing` is `charge-create`, because the
// command line has already said billing.
func opWord(service, id, derived string) string {
	if id == "" {
		return derived
	}
	name := kebab(id)
	if p := service + "-"; strings.HasPrefix(name, p) && len(name) > len(p) {
		name = name[len(p):]
	}
	return name
}

// plural is the collection test. English "ends in s" is crude, but it reads the
// same signal the route author already gave when they named the segment, so it
// tracks intent without a second place to keep in sync — and the endings that
// break it (-ss, -us, -is) are singular, which is the direction that matters:
// mistaking one thing for many is the error that produces a wrong command name.
func plural(w string) bool {
	return strings.HasSuffix(w, "s") &&
		!strings.HasSuffix(w, "ss") &&
		!strings.HasSuffix(w, "us") &&
		!strings.HasSuffix(w, "is")
}

// methodVerb is the verb a method contributes. item says the path addresses one
// thing rather than many, which is what separates "get one" from "list many".
func methodVerb(method string, item bool) string {
	switch method {
	case "GET":
		if item {
			return "get"
		}
		return "list"
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	}
	return strings.ToLower(method)
}

// isVersion reports whether a segment is an API version ("v1"), which names the
// contract rather than the resource and so is not part of any command's name.
func isVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// kebab renders a Go/JSON identifier as a flag or command word: organizationId
// → organization-id, ID → id, snake_case → snake-case.
func kebab(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == ' ' || r == '.':
			b.WriteByte('-')
			continue
		case r >= 'A' && r <= 'Z':
			prevLower := i > 0 && (rs[i-1] >= 'a' && rs[i-1] <= 'z' || rs[i-1] >= '0' && rs[i-1] <= '9')
			nextLower := i+1 < len(rs) && rs[i+1] >= 'a' && rs[i+1] <= 'z'
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('-')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), "-")
}

func sortCommands(cmds []Command) {
	sort.Slice(cmds, func(i, j int) bool {
		if cmds[i].Service != cmds[j].Service {
			return cmds[i].Service < cmds[j].Service
		}
		return cmds[i].Name < cmds[j].Name
	})
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// Invoker performs one parsed command. path holds the positional arguments by
// parameter name and body is the JSON built from the flags — exactly what a
// typed op's invoke seam takes, so a command runs through the same decode,
// validate and authorize path as a REST request or an MCP tool call.
type Invoker func(ctx context.Context, c Command, path map[string]string, body []byte) (any, error)

// LocalInvoke runs the handler in this process, through the op's own invoke
// seam. It is the whole reason a fused binary needs no client: the command IS
// the handler call.
func LocalInvoke(ctx context.Context, c Command, path map[string]string, body []byte) (any, error) {
	if c.op == nil || c.op.invoke == nil {
		return nil, fmt.Errorf("%s %s is not registered in this process — give the CLI a Remote invoker", c.Service, c.Name)
	}
	// No query map: a command's arguments are named, so they bind as path
	// values. A CLI has no URL for the "?a=b" half to have come from.
	return c.op.invoke(ctx, body, nil, path)
}

// Remote executes a command against a running zip service, and reads that
// service's registry off the document it derives from it. Base is an address in
// the same form Mount takes — the scheme selects the transport, so a command
// runs over ZAP, HTTP or anything else RegisterTransport'd without the CLI
// knowing which.
type Remote struct {
	Base   string            // "https://api.hanzo.ai", "http://:8080", a unix socket path…
	Header map[string]string // sent on every request (Authorization, …)
}

// Spec fetches the OpenAPI document — the registry in wire form. Pair it with
// CommandsFromSpec to build a command tree for a service this binary does not
// link.
func (r Remote) Spec(ctx context.Context) ([]byte, error) {
	return r.do(ctx, "GET", "/.well-known/openapi.json", nil)
}

// Invoke sends one command to the service. Its signature is Invoker's, so it
// drops into a CLI wherever LocalInvoke would.
func (r Remote) Invoke(ctx context.Context, c Command, path map[string]string, body []byte) (any, error) {
	url := c.Path
	for name, val := range path {
		url = strings.ReplaceAll(url, ":"+name, urlEscape(val))
	}
	// A GET/HEAD carries no body; its non-path inputs belong in the query
	// string, which is where the route's own decoder looks for them.
	if (c.Method == "GET" || c.Method == "HEAD") && len(body) > 0 {
		q, err := queryOf(body)
		if err != nil {
			return nil, err
		}
		if q != "" {
			url += "?" + q
		}
		body = nil
	}
	out, err := r.do(ctx, c.Method, url, body)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return json.RawMessage(out), nil
}

// do performs one request over the transport Base names. ctx is honoured up to
// the point the request is handed to the transport, which owns its own
// deadlines from there.
func (r Remote) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scheme, host, t, err := transportFor(r.Base)
	if err != nil {
		return nil, err
	}
	if t.Dial == nil {
		return nil, fmt.Errorf("zip: transport %q cannot dial (serve-only)", scheme)
	}
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(path)
	req.SetHost(host)
	req.Header.SetMethod(method)
	if scheme == "https" {
		req.URI().SetScheme("https")
	}
	if len(body) > 0 {
		req.SetBody(body)
		req.Header.SetContentType("application/json")
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	if err := t.Dial(host).Do(req, resp); err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	out := append([]byte(nil), resp.Body()...)
	if code := resp.StatusCode(); code >= 400 {
		return nil, fmt.Errorf("%s %s: %d %s", method, path, code, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// queryOf renders a flat JSON object as a query string, sorted so a command
// line always produces the same URL.
func queryOf(body []byte) (string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		v := string(m[k])
		if s, err := strconv.Unquote(v); err == nil {
			v = s
		}
		b.WriteString(urlEscape(k))
		b.WriteByte('=')
		b.WriteString(urlEscape(v))
	}
	return b.String(), nil
}

// urlEscape percent-encodes everything outside the unreserved set. Small enough
// to not be worth a net/url import in a package this hot.
func urlEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// The runner
// ---------------------------------------------------------------------------

// CLI runs a derived command tree. Name is what the usage lines call the
// binary; Invoke defaults to LocalInvoke and Out to os.Stdout's stand-in the
// caller passes.
type CLI struct {
	Name     string
	Commands []Command
	Invoke   Invoker
	Out      io.Writer
}

// CLI returns the command line for everything registered on this app, executed
// in-process. `app.CLI().Run(ctx, os.Args[1:])` is a complete CLI for a service.
func (a *App) CLI() *CLI {
	name := a.cfg.AppName
	if name == "" {
		name = "zip"
	}
	return &CLI{Name: name, Commands: a.Commands(), Invoke: LocalInvoke}
}

// Run executes one command line. It is the only entry point: help, dispatch and
// errors all come out of here so there is one place a CLI behaves.
func (c *CLI) Run(ctx context.Context, args []string) error {
	out := c.Out
	if out == nil {
		return fmt.Errorf("zip: CLI.Out is nil")
	}
	if c.Invoke == nil {
		c.Invoke = LocalInvoke
	}
	args = trimHelp(args)
	if len(args) == 0 {
		c.usage(out)
		return nil
	}
	if len(args) == 1 {
		if !c.hasService(args[0]) {
			c.usage(out)
			return fmt.Errorf("unknown service %q", args[0])
		}
		c.serviceUsage(out, args[0])
		return nil
	}

	matches := c.find(args[0], args[1])
	switch len(matches) {
	case 0:
		if c.hasService(args[0]) {
			c.serviceUsage(out, args[0])
			return fmt.Errorf("unknown operation %q for %s", args[1], args[0])
		}
		c.usage(out)
		return fmt.Errorf("unknown service %q", args[0])
	case 1:
	default:
		// Two ops that name themselves identically. Never resolved by picking
		// one: silently running the wrong route is worse than saying so.
		var paths []string
		for _, m := range matches {
			paths = append(paths, m.Method+" "+m.Path)
		}
		return fmt.Errorf("%s %s is ambiguous: %s", args[0], args[1], strings.Join(paths, ", "))
	}
	cmd := matches[0]

	rest := args[2:]
	if wantsHelp(rest) {
		cmd.help(out, c.Name)
		return nil
	}
	path, body, err := cmd.parse(rest)
	if err != nil {
		cmd.help(out, c.Name)
		return err
	}
	res, err := c.Invoke(ctx, cmd, path, body)
	if err != nil {
		return err
	}
	return writeResult(out, res)
}

// parse turns the remaining argv into the two values an op's invoke seam takes:
// the path parameters and the JSON body. Only flags that were actually given
// appear in the body — an absent flag must not become a zero that overwrites a
// server-side default.
func (c Command) parse(args []string) (map[string]string, []byte, error) {
	byName := make(map[string]Flag, len(c.Flags))
	for _, f := range c.Flags {
		byName[f.Name] = f
	}
	fields := map[string]json.RawMessage{}
	var positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		f, ok := byName[name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown flag --%s for %s %s", name, c.Service, c.Name)
		}
		if !hasVal {
			// A boolean stands alone; anything else takes the next argument.
			if f.Type == "boolean" {
				val = "true"
			} else {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("--%s needs a value", name)
				}
				i++
				val = args[i]
			}
		}
		raw, err := encodeFlag(f, val)
		if err != nil {
			return nil, nil, err
		}
		fields[f.Field] = raw
	}

	if len(positional) != len(c.Args) {
		return nil, nil, fmt.Errorf("%s %s takes %d argument(s): %s",
			c.Service, c.Name, len(c.Args), argNames(c.Args))
	}
	path := make(map[string]string, len(c.Args))
	for i, a := range c.Args {
		path[a.Name] = positional[i]
	}
	for _, f := range c.Flags {
		if f.Required {
			if _, ok := fields[f.Field]; !ok {
				return nil, nil, fmt.Errorf("--%s is required", f.Name)
			}
		}
	}
	if len(fields) == 0 {
		return path, nil, nil
	}
	body, err := json.Marshal(fields)
	return path, body, err
}

// encodeFlag turns one flag value into the JSON it stands for, refusing a value
// its type cannot hold rather than sending something the handler will reject
// with a worse message.
func encodeFlag(f Flag, val string) (json.RawMessage, error) {
	switch f.Type {
	case "string":
		b, err := json.Marshal(val)
		return b, err
	case "integer":
		if _, err := strconv.ParseInt(val, 10, 64); err != nil {
			return nil, fmt.Errorf("--%s wants an integer, got %q", f.Name, val)
		}
		return json.RawMessage(val), nil
	case "number":
		if _, err := strconv.ParseFloat(val, 64); err != nil {
			return nil, fmt.Errorf("--%s wants a number, got %q", f.Name, val)
		}
		return json.RawMessage(val), nil
	case "boolean":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("--%s wants true or false, got %q", f.Name, val)
		}
		return json.RawMessage(strconv.FormatBool(b)), nil
	default:
		if !json.Valid([]byte(val)) {
			return nil, fmt.Errorf("--%s wants JSON, got %q", f.Name, val)
		}
		return json.RawMessage(val), nil
	}
}

func writeResult(w io.Writer, res any) error {
	if res == nil {
		return nil
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func (c *CLI) find(service, name string) []Command {
	var out []Command
	for _, cmd := range c.Commands {
		if cmd.Service == service && cmd.Name == name {
			out = append(out, cmd)
		}
	}
	return out
}

func (c *CLI) hasService(service string) bool {
	for _, cmd := range c.Commands {
		if cmd.Service == service {
			return true
		}
	}
	return false
}

// trimHelp drops a leading help token so `cli help billing` and `cli billing`
// reach the same place.
func trimHelp(args []string) []string {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return args[1:]
	}
	return args
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			return true
		}
	}
	return false
}

func argNames(args []Arg) string {
	if len(args) == 0 {
		return "(none)"
	}
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = "<" + a.Name + ">"
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// Help
// ---------------------------------------------------------------------------

func (c *CLI) usage(w io.Writer) {
	fmt.Fprintf(w, "%s — %d operations, derived from the service registry\n\n", c.Name, len(c.Commands))
	fmt.Fprintf(w, "Usage:\n  %s <service> <operation> [args] [flags]\n\n", c.Name)
	if len(c.Commands) == 0 {
		fmt.Fprintf(w, "No typed operations are registered, so there is nothing to derive.\n")
		return
	}
	counts := map[string]int{}
	var services []string
	for _, cmd := range c.Commands {
		if counts[cmd.Service] == 0 {
			services = append(services, cmd.Service)
		}
		counts[cmd.Service]++
	}
	sort.Strings(services)
	fmt.Fprintf(w, "Services:\n")
	width := 0
	for _, s := range services {
		width = max(width, len(s))
	}
	for _, s := range services {
		fmt.Fprintf(w, "  %-*s  %d operations\n", width, s, counts[s])
	}
	fmt.Fprintf(w, "\nRun `%s <service>` to list its operations.\n", c.Name)
}

func (c *CLI) serviceUsage(w io.Writer, service string) {
	fmt.Fprintf(w, "%s %s\n\nUsage:\n  %s %s <operation> [args] [flags]\n\nOperations:\n",
		c.Name, service, c.Name, service)
	width := 0
	for _, cmd := range c.Commands {
		if cmd.Service == service {
			width = max(width, len(cmd.Name)+len(argNames(cmd.Args)))
		}
	}
	for _, cmd := range c.Commands {
		if cmd.Service != service {
			continue
		}
		use := cmd.Name
		if len(cmd.Args) > 0 {
			use += " " + argNames(cmd.Args)
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width+1, use, cmd.Summary)
	}
}

// help renders one command: what it does, how to call it, every flag, and an
// example built from the doc comment's own request body — so the example a
// reader copies is the example the spec ships, not a second one to keep true.
func (c Command) help(w io.Writer, bin string) {
	fmt.Fprintf(w, "%s %s %s", bin, c.Service, c.Name)
	if len(c.Args) > 0 {
		fmt.Fprintf(w, " %s", argNames(c.Args))
	}
	if len(c.Flags) > 0 {
		fmt.Fprintf(w, " [flags]")
	}
	fmt.Fprintf(w, "\n\n")
	if c.Description != "" {
		fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(c.Description))
	} else if c.Summary != "" {
		fmt.Fprintf(w, "%s\n\n", c.Summary)
	}
	fmt.Fprintf(w, "%s %s\n\n", c.Method, c.Path)

	width := 0
	for _, a := range c.Args {
		width = max(width, len(a.Name)+2)
	}
	for _, f := range c.Flags {
		width = max(width, len(f.Name)+len(f.Type)+3)
	}
	if len(c.Args) > 0 {
		fmt.Fprintf(w, "Arguments:\n")
		for _, a := range c.Args {
			fmt.Fprintf(w, "  %-*s  %s\n", width, "<"+a.Name+">", a.Help)
		}
		fmt.Fprintln(w)
	}
	if len(c.Flags) > 0 {
		fmt.Fprintf(w, "Flags:\n")
		for _, f := range c.Flags {
			help := f.Help
			if f.Required {
				help = strings.TrimSpace(help + " (required)")
			}
			fmt.Fprintf(w, "  %-*s  %s\n", width, "--"+f.Name+" "+f.Type, help)
		}
		fmt.Fprintln(w)
	}
	if ex := c.exampleLine(bin); ex != "" {
		fmt.Fprintf(w, "Example:\n  %s\n", ex)
	}
}

// exampleLine renders the doc comment's Example body as the command line that
// sends it. The example in the reference and the example on the terminal are
// then the same value, projected twice.
func (c Command) exampleLine(bin string) string {
	if len(c.Example) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(c.Example, &m) != nil {
		return ""
	}
	flag := make(map[string]Flag, len(c.Flags))
	for _, f := range c.Flags {
		flag[f.Field] = f
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s", bin, c.Service, c.Name)
	for _, a := range c.Args {
		val := "<" + a.Name + ">"
		if raw, ok := m[a.Name]; ok {
			if s, err := strconv.Unquote(string(raw)); err == nil {
				val = s
			}
		}
		fmt.Fprintf(&b, " %s", val)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f, ok := flag[k]
		if !ok {
			continue
		}
		val := string(m[k])
		if s, err := strconv.Unquote(val); err == nil {
			val = s
		}
		if strings.ContainsAny(val, " \t\"'") {
			val = strconv.Quote(val)
		}
		if f.Type == "boolean" && val == "true" {
			fmt.Fprintf(&b, " --%s", f.Name)
			continue
		}
		fmt.Fprintf(&b, " --%s %s", f.Name, val)
	}
	return b.String()
}
