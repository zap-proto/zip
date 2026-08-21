package zip

import (
	"context"
	"strings"

	"github.com/zap-proto/zip/internal/sock"
	"github.com/zap-proto/zip/internal/zapenc"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/fiber/v3"
)

// The op-call plane — the projection that lets services compose WITHOUT
// linking. The same typed-op registry (a.registry) that produces the REST routes,
// the OpenAPI document and the MCP tool list also answers a call addressed by
// the op's NAME, carrying the op's In as the body and its Out as the reply.
// One value (the op), five projections (REST · OpenAPI · MCP · CLI · call).
//
// # Why a name and not a URL
//
// A caller that reaches another service today either imports its package — and
// with it the whole dependency graph behind it, which is what turns a fleet of
// small services into one mega-binary — or hand-writes a client against its
// URLs, which is the schema copied into a second place to drift. Neither is
// necessary: the op already HAS a stable identity, [Op.OperationID], and the
// document and the tool list both key on it. So does this plane. The URL is a
// REST-shaped detail of one projection; the name is the op.
//
// # Why it is a route
//
// Because it is an ordinary route on the app, it rides EVERY transport the app
// Listens on with no extra wiring — ZAP over a unix socket is simply the
// address the caller dialed. It also inherits the app's middleware, its error
// handler, and (inside op.invoke) its validator and [Authorizer], so a call
// arriving here is authorized by the same decision the REST request is. It is
// exposed exactly as much as the REST routes are and no more.
//
// # Client
//
//	c, err := zip.DialApp("flags")               // $ZIP_RUNTIME_DIR/flags.sock
//	out, err := zip.Call[BoolIn, BoolOut](ctx, c, "flags_bool", &in)
//
// [Call] is a function rather than a method because Go methods cannot take
// type parameters — the same reason [Get] and [Post] take the *App as their
// first argument. Registration and invocation therefore read alike:
// zip.Post[In,Out](app, …) declares an op, zip.Call[In,Out](ctx, conn, …)
// invokes one.
//
// # Why generic, not generated
//
// A generated per-op client method would put a SECOND copy of the schema in
// the caller's repository, regenerated on its own schedule — the drift this
// plane exists to remove, reintroduced one directory over. The generic call is
// type-safe at the call site against the same In/Out the callee declared, and
// there is nothing to regenerate. When the caller wants those types checked at
// compile time it imports the op's In/Out — a types-only package with none of
// the handler's dependencies — and the call is fully typed end to end with no
// generator in the loop. Generating a Go client per app would also be a fourth
// client generator racing openapi-generator, which already produces the
// published SDKs from this same registry's OpenAPI projection.

// CallPath is where the op-call plane is mounted. It sits under /.well-known/
// with the OpenAPI document because it is control plane, not application
// surface: an app owns its own path space and must never find zip squatting in
// it.
const CallPath = "/.well-known/zip/op/"

// CallContentType marks a body on the op-call plane. It names ZAP explicitly so
// a request that arrives with anything else is a caller that has not been
// updated, rather than a body silently read under the wrong codec.
const CallContentType = "application/zap"

// callFault is how a refusal crosses the plane: the status the op chose, its
// code and its message, as ZAP like everything else here. The REST error
// handler renders JSON for browsers; this plane never does.
type callFault struct {
	Status int32
	Code   string
	Msg    string
}

// sendCallError writes a refusal in the plane's own encoding, preserving the
// status the op chose — 402 vs 403 vs 404 vs 503 is the whole answer, and a
// plane that collapsed them would make every failure look alike.
func sendCallError(fc fiber.Ctx, err error) error {
	f := callFault{Status: 500, Msg: err.Error()}
	if he, ok := asHTTPError(err); ok {
		f.Status, f.Code, f.Msg = int32(he.Status), he.Code, he.Msg
	}
	body, merr := zapenc.Marshal(&f)
	if merr != nil {
		return err // fall back to the app's own handler rather than lose the error
	}
	fc.Set(fiber.HeaderContentType, CallContentType)
	fc.Status(int(f.Status))
	return fc.Send(body)
}

// RuntimeDirEnv names the directory holding one socket per app. It is the one
// knob in the socket-path scheme; see [SocketPath].
const RuntimeDirEnv = sock.DirEnv

// RuntimeDir is the directory that holds the fleet's sockets, resolved in one
// order:
//
//	$ZIP_RUNTIME_DIR        set explicitly — always wins
//	$XDG_RUNTIME_DIR/zip    a developer's per-user runtime dir (/run/user/1000)
//	/run/zip                the system default
//
// The middle case is what makes a dev box work without configuration: /run/zip
// is not writable by a normal user and $XDG_RUNTIME_DIR is.
func RuntimeDir() string { return sock.Dir() }

// SocketPath is the ONE canonical mapping from a service NAME to the unix
// socket it is reachable at:
//
//	<RuntimeDir()>/<name>.sock
//
// Both halves use it, which is what keeps them from drifting: a service serves
// at zip.Addr(zip.SocketPath("flags")) and a caller reaches it with
// zip.DialApp("flags"). There is no registry, no discovery service and no
// second spelling — the name IS the address, and one environment variable
// moves the whole fleet.
func SocketPath(name string) string { return sock.Path(name) }

// socketIn is the shape of every zip socket path, in one place: the plugin
// loader names a child's private socket with it too (see start), so "a
// service's socket is <dir>/<name>.sock" is stated once.
func socketIn(dir, name string) string { return sock.In(dir, name) }

// socketFits is the length rule, kept beside the shape rule because both are
// properties of the same address: minting one is not enough, it also has to be
// bindable, and whoever mints it is the only one positioned to say so.
func socketFits(path string) error { return sock.Fits(path) }

// installCallPlane mounts the by-name op plane when there are ops to call.
// Called from prepare() alongside installOpenAPIRoutes and installMCP.
func (a *App) installCallPlane() {
	if len(a.Registry()) == 0 {
		return
	}
	a.control(fiber.MethodPost, CallPath+":op", func(fc fiber.Ctx) error {
		// opByName is the ONE by-name lookup and op.invoke the ONE dispatcher —
		// the same pair tools/call runs, without MCP's agent-facing envelope.
		op := a.opByName(fc.Params("op"))
		if op == nil || op.invoke == nil {
			return sendCallError(fc, ErrNotFound("unknown op: "+fc.Params("op")))
		}
		// The whole input arrives as the body, exactly as it does over MCP:
		// addressing by name means there is no URL to carry half of it.
		//
		// THE BODY IS ZAP, and only ZAP. This plane is service-to-service, so it
		// carries the same bytes both ends hold in memory rather than serializing
		// through a text format on the way. JSON is the BOUNDARY encoding — it
		// belongs on the REST routes a browser reaches and in the MCP envelope an
		// agent reads, and has no place inside the binary protocol.
		out, err := op.invoke(callerContext(fc), zapenc.Unmarshal, fc.Body(), nil, nil, func(k string) string { return fc.Get(k) })
		if err != nil {
			return sendCallError(fc, err)
		}
		if out == nil {
			return fc.SendStatus(fiber.StatusNoContent)
		}
		// The call plane owns this response — one request, one op — so a declared
		// response header is written here exactly as it is over REST.
		hdrs, herr := responseHeadersOf(op, out)
		if herr != nil {
			return sendCallError(fc, herr)
		}
		for name, v := range hdrs {
			fc.Set(name, v)
		}
		body, merr := zapenc.Marshal(out)
		if merr != nil {
			return sendCallError(fc, ErrInternal("zip: encode reply: "+merr.Error()))
		}
		fc.Set(fiber.HeaderContentType, CallContentType)
		return fc.Send(body)
	})
	a.logger.Info("zip call plane", "path", CallPath, "ops", len(a.Registry()))
}

// Conn is a handle to another zip app, dialed once and used concurrently. It
// holds a pooled transport, so a call on a warm Conn costs a round trip and no
// dial. The zero value is unusable; get one from [Dial] or [DialApp].
type Conn struct {
	name   string // authority the callee sees; the app name when dialed by name
	addr   string
	scheme string
	client Client
}

// Dial returns a Conn to the app at addr. The address scheme selects the
// transport exactly as it does for [App.Listen] and [Proxy] — one registry,
// one vocabulary — so a bare path is ZAP over a unix socket and a bare host:port
// is ZAP over tcp:
//
//	zip.Dial("/run/zip/flags.sock")            // ZAP over unix
//	zip.Dial("flags.hanzo.svc:9653")           // ZAP over tcp
//	zip.Dial("https://api.hanzo.ai")           // an external edge
//
// Dialing is lazy: the connection opens on the first [Call].
func Dial(addr string) (*Conn, error) {
	scheme, a, t, err := transportFor(addr)
	if err != nil {
		return nil, err
	}
	if t.Dial == nil {
		return nil, Errorf(500, "zip: transport %q cannot dial (serve-only)", scheme)
	}
	return &Conn{name: a, addr: a, scheme: scheme, client: t.Dial(a)}, nil
}

// DialApp returns a Conn to the named app over its canonical unix socket — the
// [SocketPath] scheme, the same one the app serves at. This is the call an
// aggregator makes instead of importing the app's package.
func DialApp(name string) (*Conn, error) {
	c, err := Dial(SocketPath(name))
	if err != nil {
		return nil, err
	}
	c.name = name
	return c, nil
}

// Addr reports the address this Conn dials.
func (c *Conn) Addr() string { return c.addr }

// Close releases the Conn's pooled connections. Calls already in flight are
// unaffected, and the Conn stays usable — a later Call redials.
func (c *Conn) Close() error {
	if ic, ok := c.client.(idleCloser); ok {
		ic.CloseIdleConnections()
	}
	return nil
}

// Call invokes op on the app behind c and decodes its reply into Out. It is
// the typed round trip: In is marshalled to the op's JSON body, the callee
// decodes it into the very same type it declared, and its Out comes back
// decoded here.
//
// A void op (one whose handler returns a nil *Out) yields a nil *Out and a nil
// error, matching what the handler returned.
//
// A handler error arrives as the [HTTPError] the handler returned — status,
// code and message intact — so errors.As on the caller's side sees what the
// callee raised rather than a stringified copy. A transport failure is a 502.
//
// Identity is not invented here. Whatever the gateway asserted about the
// caller rides along when ctx carries a request (see [Ctx.Forward]); WHICH
// process is calling is answered by the kernel at the other end, from the
// socket's peer credential (see [Peer]), not by anything this client could
// set.
//
// ctx is honored to the extent the transport allows: an already-cancelled ctx
// fails before the wire, and the transport's own read timeout bounds the call
// (zap: 30s). It is not cancellable mid-flight, because abandoning a call
// would mean abandoning the pooled buffers it is writing into.
func Call[In, Out any](ctx context.Context, c *Conn, op string, in *In) (*Out, error) {
	if c == nil || c.client == nil {
		return nil, ErrInternal("zip: Call on a nil Conn")
	}
	if !validOpName(op) {
		return nil, ErrBadRequest("zip: invalid op name " + op)
	}
	if err := ctx.Err(); err != nil {
		return nil, Errorf(499, "zip: call %s: %v", op, err)
	}

	var body []byte
	if in != nil {
		b, err := zapenc.Marshal(in)
		if err != nil {
			return nil, ErrBadRequest("zip: encode " + op + ": " + err.Error())
		}
		body = b
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType(CallContentType)
	req.SetHost(c.name)
	req.URI().SetPath(CallPath + op)
	req.SetBody(body)
	forwardIdentity(ctx, req)

	if err := c.client.Do(req, resp); err != nil {
		// The far end, not this hop, is what failed — same reading Proxy gives a
		// broken upstream.
		return nil, Errorf(502, "zip: call %s at %s: %v", op, c.addr, err)
	}

	switch code := resp.StatusCode(); {
	case code == fasthttp.StatusNoContent:
		return nil, nil
	case code < 200 || code > 299:
		return nil, remoteError(code, op, resp.Body())
	}
	if len(resp.Body()) == 0 {
		return nil, nil
	}
	var out Out
	if err := zapenc.Unmarshal(resp.Body(), &out); err != nil {
		return nil, Errorf(502, "zip: decode %s reply: %v", op, err)
	}
	return &out, nil
}

// remoteError rebuilds the refusal the callee chose, so errors.As on this side
// sees the *HTTPError the other side returned with its status intact. The body
// is ZAP like everything else on this plane; a body that does not decode leaves
// the transport's own status, which is still an honest answer.
func remoteError(code int, op string, body []byte) error {
	var f callFault
	if err := zapenc.Unmarshal(body, &f); err != nil || f.Msg == "" {
		return Errorf(code, "zip: call %s: %s", op, body)
	}
	status := int(f.Status)
	if status == 0 {
		status = code
	}
	return &HTTPError{Status: status, Code: f.Code, Msg: f.Msg}
}

// validOpName rejects a name that would address something other than an op.
// An operation id is an identifier — the OpenAPI document, the MCP tool list
// and this plane all key on the same token — so a name carrying URL syntax is
// a caller bug, caught here rather than becoming a mysterious 404 or, worse, a
// request to a different route.
func validOpName(op string) bool {
	return op != "" && !strings.ContainsAny(op, "/?#% \t\r\n")
}
