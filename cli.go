package zip

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	neturl "net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"

	"github.com/zap-proto/zip/internal/jsonenc"
	"github.com/zap-proto/zip/internal/ws"
)

// CLI — the FOURTH projection. The same typed-op registry (a.registry) that produces
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
	// Service is the first non-version segment of the path ("billing").
	Service string
	// Name is the operation token under the service ("invoices-list").
	Name string

	// A command is a projection of an op, not a second thing with a second
	// name, and its operation id is what says WHICH op it projects.

	// OperationID is the operation's identity: the same token the document's
	// operationId and the MCP tool's name carry.
	OperationID string

	// Summary is the one-line help, from the handler's doc comment.
	Summary string
	// Description is the full prose, from the same doc comment.
	Description string

	// Method is the operation's HTTP method.
	Method string
	// Path is the operation's route pattern, its parameters as ":name".
	Path string

	// The URL is the addressing authority (see bindPath), so what addresses
	// the resource is positional and what modifies the request is a flag.

	// Args are the path parameters, in path order, as positional arguments.
	Args []Arg

	// Flags are the operation's other inputs, one flag each.
	Flags []Flag

	// A spec-derived command reads its example from the request body or, for a
	// bodyless method, rebuilds it from the parameters the document had to
	// split it across.

	// Example is the operation's example input, which the help renders as a
	// runnable command line.
	Example json.RawMessage

	// Consumes are the media the request body is sent in when it is not
	// application/json, sorted: bytes as sent for --body, a form for form
	// fields and --field parts.
	Consumes []string `json:",omitempty"`

	// Stream is sse, bytes or socket when the answer is not one JSON value: the
	// runner prints events as they arrive, writes the bytes, or bridges the
	// connection to standard input and output.
	Stream string `json:",omitempty"`

	// op is set only by App.Commands: it is what LocalInvoke runs. A
	// spec-derived command has none and must be executed remotely.
	op *registeredOp
}

// Arg is one path parameter as a positional argument.
type Arg struct {
	// Name is the path parameter's name, "app".
	Name string
	// Help is the parameter's prose, from the field it binds.
	Help string
}

// Flag is one In field as a flag.
type Flag struct {
	// Name is the flag, kebab-cased and without the dashes: "organization-id".
	Name string
	// Field is the name it is sent under: the JSON field, the header, the
	// cookie or the form key.
	Field string
	// Type is the kind of value it takes: string, integer, number, boolean,
	// json or file.
	Type string
	// Help is the flag's prose, from the field it sets.
	Help string
	// Required says the command refuses to run without it.
	Required bool
	// In is where the value rides: empty for the JSON body or the query, cookie
	// for a request cookie, form for a form field, file for a multipart part
	// (--field name=@path), body for the request body itself (--body @path, or
	// - for standard input).
	In string `json:",omitempty"`
}

// Commands projects every registered typed op into a command. This is the whole
// derivation: no registration, no list of commands, no per-endpoint code.
func (a *App) Commands() []Command {
	cmds := ProjectCLI(a.Manifest())
	// What a description cannot carry: the handler. A command spelled
	// here runs in this process, so the op it projects is attached by the id
	// both halves already address it by.
	ops := make(map[string]*registeredOp, len(cmds))
	for _, op := range a.Registry() {
		ops[opName(op)] = op
	}
	for i := range cmds {
		cmds[i].op = ops[cmds[i].OperationID]
	}
	return cmds
}

func isParam(params []pathParam, name string) bool {
	for _, p := range params {
		if strings.EqualFold(p.Name, name) || strings.EqualFold(p.Key, name) {
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
	if id == ID(method, path) {
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
			// A word a separator already opened takes no second one: a header
			// is X-Org-Id, and its flag is x-org-id.
			opened := i > 0 && strings.ContainsRune("-_ .", rs[i-1])
			if i > 0 && !opened && (prevLower || nextLower) {
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
//
// A command's arguments are named, so they bind as path values, and a CLI has
// no URL for the "?a=b" half: everything else arrives as one argument object,
// exactly as it does over MCP. A flag that rides as a cookie is handed to the
// op as one, the only way a cookie field is set.
func LocalInvoke(ctx context.Context, c Command, path map[string]string, body []byte) (any, error) {
	if c.op == nil || c.op.invoke == nil {
		return nil, fmt.Errorf("%s %s is not registered in this process — give the CLI a Remote invoker", c.Service, c.Name)
	}
	rest, cookie, err := c.split(body)
	if err != nil {
		return nil, err
	}
	// The body flag is sent under "body", which the document can name; the
	// op's own field may be named otherwise.
	if raw := c.op.req.raw; raw != nil {
		if v, ok := rest["body"]; ok {
			delete(rest, "body")
			rest[jsonFieldName(deref(c.op.InType).FieldByIndex(raw))] = v
		}
	}
	wire := input{dec: jsonenc.Unmarshal, media: mimeJSON, path: path,
		cookie: func(k string) string { return cookie[k] }}
	if len(rest) > 0 {
		if wire.body, err = json.Marshal(rest); err != nil {
			return nil, err
		}
	}
	return c.op.invoke(withOp(ctx, servedOp(c.op)), wire)
}

// split takes a command's argument object apart: the values that ride as
// cookies, keyed by the cookie, and the rest.
func (c Command) split(body []byte) (rest map[string]json.RawMessage, cookie map[string]string, err error) {
	rest = map[string]json.RawMessage{}
	cookie = map[string]string{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &rest); err != nil {
			return nil, nil, fmt.Errorf("%s %s: the arguments are not an object: %w", c.Service, c.Name, err)
		}
	}
	for _, f := range c.Flags {
		raw, ok := rest[f.Field]
		if !ok || f.In != "cookie" {
			continue
		}
		v, err := queryValue(raw)
		if err != nil {
			return nil, nil, err
		}
		cookie[f.Field] = v
		delete(rest, f.Field)
	}
	return rest, cookie, nil
}

// Remote executes a command against a running zip service, and reads that
// service's registry off the document it derives from it. Base is an address in
// the same form Proxy takes — the scheme selects the transport, so a command
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
//
// The request is built the way the op takes it: cookie flags as cookies; a
// --body as the bytes themselves under the media the op consumes; form fields
// and --field parts as a form; the rest as the JSON body, or as the query when
// the method carries no body or the body is not JSON. An answer that is not one
// JSON value comes back as it arrives — a [Body] whose Reader the runner
// writes, events as the stream yields them, a [Redirect] for a 3xx with no
// body, and for an op that upgrades, the open connection.
func (r Remote) Invoke(ctx context.Context, c Command, path map[string]string, body []byte) (any, error) {
	url := c.Path
	for name, val := range path {
		url = strings.ReplaceAll(url, ":"+name, urlEscape(val))
	}
	rest, cookie, err := c.split(body)
	if err != nil {
		return nil, err
	}
	var payload []byte
	var media string
	kind := ""
	for _, f := range c.Flags {
		switch f.In {
		case "body", "form", "file":
			kind = f.In
		}
		if kind == "body" {
			break
		}
	}
	switch {
	case kind == "body":
		if raw, ok := rest["body"]; ok {
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, fmt.Errorf("--body: %w", err)
			}
			delete(rest, "body")
		}
		media = mimeOctet
		if len(c.Consumes) > 0 {
			media = c.Consumes[0]
		}
	case kind == "form" || kind == "file":
		payload, media, err = c.form(rest)
		if err != nil {
			return nil, err
		}
	case hasBody(c.Method) && c.Stream != streamSocket:
		if len(rest) > 0 {
			if payload, err = json.Marshal(rest); err != nil {
				return nil, err
			}
			media = mimeJSON
			if len(c.Consumes) > 0 && !containsString(c.Consumes, mimeJSON) {
				media = c.Consumes[0]
			}
		}
		rest = nil
	}
	if len(rest) > 0 {
		b, err := json.Marshal(rest)
		if err != nil {
			return nil, err
		}
		q, err := queryOf(b)
		if err != nil {
			return nil, err
		}
		if q != "" {
			url += "?" + q
		}
	}
	if c.Stream == streamSocket {
		return r.dial(ctx, url, cookie)
	}
	return r.send(ctx, c, url, payload, media, cookie)
}

// form encodes a command's form fields and file parts, and takes them out of
// rest: url-encoded, or multipart when the op consumes it or a part is a file.
func (c Command) form(rest map[string]json.RawMessage) ([]byte, string, error) {
	multi := containsString(c.Consumes, mimeMultipart)
	for _, f := range c.Flags {
		if f.In == "file" {
			multi = true
		}
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	values := neturl.Values{}
	for _, f := range c.Flags {
		raw, ok := rest[f.Field]
		if !ok || (f.In != "form" && f.In != "file") {
			continue
		}
		delete(rest, f.Field)
		if f.In == "file" {
			var parts []File
			if raw[0] == '[' {
				if err := json.Unmarshal(raw, &parts); err != nil {
					return nil, "", fmt.Errorf("--field %s: %w", f.Field, err)
				}
			} else {
				var one File
				if err := json.Unmarshal(raw, &one); err != nil {
					return nil, "", fmt.Errorf("--field %s: %w", f.Field, err)
				}
				parts = []File{one}
			}
			for _, part := range parts {
				h := textproto.MIMEHeader{}
				h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.Field, part.Name))
				h.Set("Content-Type", nonEmpty(part.Type, mimeOctet))
				w, err := mw.CreatePart(h)
				if err != nil {
					return nil, "", err
				}
				if _, err := w.Write(part.Bytes); err != nil {
					return nil, "", err
				}
			}
			continue
		}
		vals, err := argValues(raw)
		if err != nil {
			return nil, "", fmt.Errorf("--%s: %w", f.Name, err)
		}
		for _, v := range vals {
			values.Add(f.Field, v)
			if multi {
				if err := mw.WriteField(f.Field, v); err != nil {
					return nil, "", err
				}
			}
		}
	}
	if !multi {
		return []byte(values.Encode()), mimeForm, nil
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

// send performs one request and reads its answer the way the command says it
// comes back.
func (r Remote) send(ctx context.Context, c Command, path string, body []byte, media string, cookie map[string]string) (any, error) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	if len(body) > 0 {
		req.SetBody(body)
		req.Header.SetContentType(media)
	}
	for k, v := range cookie {
		req.Header.SetCookie(k, v)
	}
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = c.Stream != ""
	if err := r.exchange(ctx, c.Method, path, req, resp); err != nil {
		fasthttp.ReleaseResponse(resp)
		return nil, err
	}
	code := resp.StatusCode()
	// A redirect carries no body, and is answered as where it sends the
	// client; a 3xx that carries one is read as any other answer is.
	if location := resp.Header.Peek("Location"); code >= 300 && code < 400 && len(location) > 0 && len(resp.Body()) == 0 {
		to := string(location)
		fasthttp.ReleaseResponse(resp)
		return &Redirect{To: to, Status: code}, nil
	}
	ct := string(resp.Header.ContentType())
	if c.Stream == "" || code >= 400 || code == fasthttp.StatusNoContent {
		out := append([]byte(nil), resp.Body()...)
		fasthttp.ReleaseResponse(resp)
		if code >= 400 {
			return nil, fmt.Errorf("%s %s: %d %s", c.Method, path, code, strings.TrimSpace(string(out)))
		}
		if len(out) == 0 {
			return nil, nil
		}
		if c.Stream == "" && !textual(ct) || c.Stream == streamBytes {
			return &Body{Type: ct, Bytes: out}, nil
		}
		return json.RawMessage(out), nil
	}
	if s := resp.BodyStream(); s != nil {
		return &Body{Type: ct, Reader: &answerStream{Reader: s, resp: resp}}, nil
	}
	out := append([]byte(nil), resp.Body()...)
	fasthttp.ReleaseResponse(resp)
	return &Body{Type: ct, Bytes: out}, nil
}

// answerStream is a streamed answer's body, holding its response until it is
// closed.
type answerStream struct {
	io.Reader
	resp *fasthttp.Response
}

func (a *answerStream) Close() error {
	err := a.resp.CloseBodyStream()
	fasthttp.ReleaseResponse(a.resp)
	return err
}

// dial opens the WebSocket an op upgrades to, over the address Base names.
func (r Remote) dial(ctx context.Context, path string, cookie map[string]string) (*ws.Conn, error) {
	scheme, host, _, err := transportFor(r.Base)
	if err != nil {
		return nil, err
	}
	var url string
	switch scheme {
	case "http":
		url = "ws://" + host + path
	case "https":
		url = "wss://" + host + path
	default:
		return nil, fmt.Errorf("zip: a WebSocket is reached over http or https, and %q is %s", r.Base, scheme)
	}
	h := http.Header{}
	for k, v := range r.Header {
		h.Set(k, v)
	}
	for k, v := range cookie {
		h.Add("Cookie", (&http.Cookie{Name: k, Value: v}).String())
	}
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, url, h)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return conn, nil
}

// do performs one request over the transport Base names, bounded by ctx.
func (r Remote) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	if len(body) > 0 {
		req.SetBody(body)
		req.Header.SetContentType("application/json")
	}
	if err := r.exchange(ctx, method, path, req, resp); err != nil {
		return nil, err
	}
	out := append([]byte(nil), resp.Body()...)
	if code := resp.StatusCode(); code >= 400 {
		return nil, fmt.Errorf("%s %s: %d %s", method, path, code, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// exchange sends req and fills resp over the transport Base names.
func (r Remote) exchange(ctx context.Context, method, path string, req *fasthttp.Request, resp *fasthttp.Response) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scheme, host, t, err := transportFor(r.Base)
	if err != nil {
		return err
	}
	if t.Dial == nil {
		return fmt.Errorf("zip: transport %q cannot dial (serve-only)", scheme)
	}
	req.SetRequestURI(path)
	req.SetHost(host)
	req.Header.SetMethod(method)
	// THE ESCAPING MUST SURVIVE THE TRANSPORT. Invoke percent-encodes every path
	// argument (urlEscape) precisely so an argument cannot become part of the
	// path, and fasthttp then undid it: its default serialisation is
	// appendQuotedPath(u.Path()), and Path() has already decoded %2F and resolved
	// "..". So `Invoke(cmd, {"app": "../../../v1/iam/users"})` on
	// GET /v1/platform/apps/:app left as GET /v1/iam/users — one op's argument
	// addressing another op entirely, on the caller's own credential.
	//
	// Set here so RequestURI() serialises PathOriginal(), the bytes Invoke built.
	// It is stated AGAIN on the http/https HostClients (transport.go), and that is
	// not a belt on a brace: HostClient.doNonNilReqResp ASSIGNS this field from
	// its own, so for those two transports the request's own wish is overwritten
	// and only the client's setting is read. This line is what carries the
	// property over every OTHER transport — ZAP, the default, and anything
	// RegisterTransport adds — which serialise the request as it was built.
	//
	// A command addresses ONE operation. That is the whole basis on which any
	// caller decides whether it may run, and a transport that re-reads the address
	// makes that decision about a different request than the one it sends.
	req.URI().DisablePathNormalizing = true
	if scheme == "https" {
		req.URI().SetScheme("https")
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	if err := do(ctx, t.Dial(host), req, resp); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func nonEmpty(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

// Query is the query string that carries in to a bodyless op.
//
// It is the client half of the URL binder and its exact inverse: what this
// writes, [bindURL] reads. That matters because they are two halves of one
// rule and they had drifted — a list went out as `?ids=["a","b"]` and a record
// as `?startIndex={"utxo":...}`, neither of which the binder reads, so the
// argument arrived empty and the op answered 200 about nothing.
//
// The value is taken from the op's own In, so the spelling is derived rather
// than agreed: a caller does not have to know that a list is comma-joined or
// that a record's leaves are named through it.
func Query(in any) (string, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return queryOf(body)
}

// queryOf renders a JSON object as a query string, sorted so a command line
// always produces the same URL.
func queryOf(body []byte) (string, error) {
	var b strings.Builder
	if err := writeQuery(&b, "", body); err != nil {
		return "", err
	}
	return b.String(), nil
}

// writeQuery writes one JSON object's keys under prefix, descending into a
// nested object the way [bindRecord] descends into a nested record.
func writeQuery(b *strings.Builder, prefix string, body []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		raw := m[k]
		if len(raw) == 0 || string(raw) == "null" {
			continue // an absent value is written by leaving the key out
		}
		name := prefix + k
		if raw[0] == '{' {
			if err := writeQuery(b, name+".", raw); err != nil {
				return err
			}
			continue
		}
		val, err := queryValue(raw)
		if err != nil {
			return err
		}
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(urlEscape(name))
		b.WriteByte('=')
		b.WriteString(urlEscape(val))
	}
	return nil
}

// queryValue is one JSON value as the characters a URL carries: a string
// unquoted, a list comma-joined, anything else as it was written.
func queryValue(raw json.RawMessage) (string, error) {
	if raw[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return "", err
		}
		parts := make([]string, len(items))
		for i, item := range items {
			part, err := queryValue(item)
			if err != nil {
				return "", err
			}
			parts[i] = part
		}
		return strings.Join(parts, ","), nil
	}
	if s, err := strconv.Unquote(string(raw)); err == nil {
		return s, nil
	}
	return string(raw), nil
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
	// In is the standard input: what `--body -` reads, and what a command
	// that opens a WebSocket sends, one text message per line. Nil reads
	// nothing.
	In io.Reader
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
	path, body, to, media, err := cmd.parse(rest, c.In)
	if err != nil {
		cmd.help(out, c.Name)
		return err
	}
	if containsString(cmd.Consumes, media) {
		// The file's own type, when the op takes it, is what the body is sent
		// as; otherwise the first the op names.
		cmd.Consumes = append([]string{media}, cmd.Consumes...)
	}
	res, err := c.Invoke(ctx, cmd, path, body)
	if err != nil {
		return err
	}
	if to != "" {
		f, err := os.Create(to)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		out = f
	}
	return writeResult(ctx, out, c.In, res)
}

// parse turns the remaining argv into the two values an op's invoke seam takes:
// the path parameters and the JSON body. Only flags that were actually given
// appear in the body — an absent flag must not become a zero that overwrites a
// server-side default.
//
// Bytes are named, not typed: `--body @file` sends a file as the request body
// (`--body -` the standard input), and `--field name=@file` sends one as a
// multipart part, as curl spells both. Each crosses as its base64 string, the
// form an argument object carries bytes in. `--out file` writes an answer that
// is not one JSON value to that file instead of the output; it is the runner's
// own flag, read only where the op names none of that name.
func (c Command) parse(args []string, stdin io.Reader) (path map[string]string, body []byte, to, media string, err error) {
	byName := make(map[string]Flag, len(c.Flags))
	parts := map[string]Flag{}
	for _, f := range c.Flags {
		if f.In == "file" {
			parts[f.Field] = f
			continue
		}
		byName[f.Name] = f
	}
	fields := map[string]json.RawMessage{}
	files := map[string][]File{}
	var positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		f, ok := byName[name]
		runner := !ok && (name == "out" || name == "field" && len(parts) > 0)
		if !ok && !runner {
			return nil, nil, "", "", fmt.Errorf("unknown flag --%s for %s %s", name, c.Service, c.Name)
		}
		if !hasVal {
			// A boolean stands alone; anything else takes the next argument.
			if ok && f.Type == "boolean" {
				val = "true"
			} else {
				if i+1 >= len(args) {
					return nil, nil, "", "", fmt.Errorf("--%s needs a value", name)
				}
				i++
				val = args[i]
			}
		}
		switch {
		case name == "out" && !ok:
			to = val
			continue
		case name == "field" && !ok:
			part, from, _ := strings.Cut(val, "=")
			pf, known := parts[part]
			if !known {
				return nil, nil, "", "", fmt.Errorf("--field %s: %s %s takes no part of that name", part, c.Service, c.Name)
			}
			b, rerr := readNamed(pf.Name, from, stdin)
			if rerr != nil {
				return nil, nil, "", "", rerr
			}
			files[pf.Field] = append(files[pf.Field], File{Name: filepath.Base(strings.TrimPrefix(from, "@")), Type: mediaOf(from), Bytes: b})
			continue
		}
		if f.In == "body" {
			media = mediaOf(val)
			b, rerr := readNamed(f.Name, val, stdin)
			if rerr != nil {
				return nil, nil, "", "", rerr
			}
			raw, merr := json.Marshal(b)
			if merr != nil {
				return nil, nil, "", "", merr
			}
			fields[f.Field] = raw
			continue
		}
		raw, ferr := encodeFlag(f, val)
		if ferr != nil {
			return nil, nil, "", "", ferr
		}
		fields[f.Field] = raw
	}
	for name, got := range files {
		var raw []byte
		var merr error
		if len(got) == 1 {
			raw, merr = json.Marshal(got[0])
		} else {
			raw, merr = json.Marshal(got)
		}
		if merr != nil {
			return nil, nil, "", "", merr
		}
		fields[name] = raw
	}

	if len(positional) != len(c.Args) {
		return nil, nil, "", "", fmt.Errorf("%s %s takes %d argument(s): %s",
			c.Service, c.Name, len(c.Args), argNames(c.Args))
	}
	path = make(map[string]string, len(c.Args))
	for i, a := range c.Args {
		path[a.Name] = positional[i]
	}
	for _, f := range c.Flags {
		if f.Required {
			if _, ok := fields[f.Field]; !ok {
				return nil, nil, "", "", fmt.Errorf("--%s is required", f.Name)
			}
		}
	}
	if len(fields) == 0 {
		return path, nil, to, media, nil
	}
	body, err = json.Marshal(fields)
	return path, body, to, media, err
}

// readNamed reads the bytes a flag names: @path is the file at path, and - is
// the standard input.
func readNamed(flag, val string, stdin io.Reader) ([]byte, error) {
	switch {
	case val == "-":
		if stdin == nil {
			return nil, fmt.Errorf("--%s -: there is no standard input", flag)
		}
		return io.ReadAll(stdin)
	case strings.HasPrefix(val, "@"):
		return os.ReadFile(val[1:])
	}
	return nil, fmt.Errorf("--%s takes @file or -, not %q", flag, val)
}

// mediaOf is the media type a file's extension names, octet-stream when it
// names none.
func mediaOf(path string) string {
	if m := mime.TypeByExtension(filepath.Ext(path)); m != "" {
		return m
	}
	return mimeOctet
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

// writeResult prints an answer. One JSON value is printed indented. Bytes are
// written as they are, as they arrive when they stream. An event stream is
// printed one event per line as each arrives. A redirect prints where it sends
// the client. An open WebSocket is bridged: each line of in is sent as a text
// message and each message received is printed on a line, until either side
// ends.
func writeResult(ctx context.Context, w io.Writer, in io.Reader, res any) error {
	res = unwrap(res)
	if res == nil {
		return nil
	}
	switch x := res.(type) {
	case redirector:
		_, err := fmt.Fprintln(w, x.redirect().To)
		return err
	case *ws.Conn:
		return bridge(ctx, x, in, w)
	case upgrader:
		return fmt.Errorf("zip: %s; reach it over HTTP", NotACall)
	case stream:
		return x.events(ctx, false, func(frame []byte) error {
			_, err := fmt.Fprintln(w, string(frame))
			return err
		})
	}
	if b, ok := bodyOf(res); ok {
		if b.Reader == nil {
			_, err := w.Write(b.Bytes)
			return err
		}
		if c, ok := b.Reader.(io.Closer); ok {
			defer func() { _ = c.Close() }()
		}
		_, err := io.Copy(w, b.Reader)
		return err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// bridge joins a WebSocket to a terminal: lines in, messages out.
func bridge(ctx context.Context, conn *ws.Conn, in io.Reader, w io.Writer) error {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if in != nil {
		go func() {
			lines := bufio.NewScanner(in)
			for lines.Scan() {
				if conn.WriteMessage(websocket.TextMessage, lines.Bytes()) != nil {
					return
				}
			}
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		}()
	}
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if _, err := fmt.Fprintln(w, string(msg)); err != nil {
			return err
		}
	}
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
		width = max(width, len(f.usage()))
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
			fmt.Fprintf(w, "  %-*s  %s\n", width, f.usage(), help)
		}
		fmt.Fprintln(w)
	}
	if c.Stream != "" && c.Stream != streamSocket {
		fmt.Fprintf(w, "The answer is written to the output as it arrives; --out <file> writes it to a file.\n\n")
	}
	if ex := c.exampleLine(bin); ex != "" {
		fmt.Fprintf(w, "Example:\n  %s\n", ex)
	}
}

// usage is how a flag is written on a command line.
func (f Flag) usage() string {
	switch f.In {
	case "body":
		return "--" + f.Name + " @file|-"
	case "file":
		return "--field " + f.Field + "=@file"
	}
	return "--" + f.Name + " " + f.Type
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
