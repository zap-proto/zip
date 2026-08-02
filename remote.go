package zip

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/valyala/fasthttp"
	fiber "github.com/zap-proto/fiber/v3"
)

// A remote service is a LEAF, not a verb.
//
// [App.Mount] used to be one of five composition verbs and the only one that
// pointed at another process. Under one program it stops being special: it
// appends an App like any other inclusion, whose routes proxy and whose ops
// forward. Every projection then reads it through the same walk — a mounted
// service appears in the OpenAPI document, the MCP tool list, the CLI commands
// and the by-name call plane because it is in the registry, not because four
// projections each learned about mounting.
//
// # The declaration is an INPUT, never a fetch
//
// The remote is described by a [Declaration] the caller supplies. Nothing here
// dials at build time, and that is deliberate to the point of being the main
// design constraint:
//
//   - a walk that did I/O would make [App.Registry] fallible, slow and
//     untestable, and every projection downstream of it too;
//   - the OpenAPI document would depend on some other process being reachable
//     at boot, so a cold start in the wrong order publishes a smaller API;
//   - the seal could not mean anything, because what the program IS would
//     depend on what the network said at the moment it was asked.
//
// The spec is a build input; the server is not asked what it serves. Without a
// declaration a mount contributes its two proxy addresses and no ops, which is
// exactly what it contributed before and says honestly that nothing is known
// about the shape behind it.
func remoteApp(parent *App, prefix, addr string, d Declaration) (*App, error) {
	scheme, hostport, t, err := transportFor(addr)
	if err != nil {
		return nil, err
	}
	if t.Dial == nil {
		return nil, Errorf(500, "zip: transport %q cannot dial (serve-only)", scheme)
	}
	client := t.Dial(hostport)

	cfg := parent.groupConfig()
	cfg.AppName = cmp.Or(d.Name, addr)
	r := newApp(cfg)

	proxy := func(c *Ctx) error {
		return forward(c.fc.Request(), c.fc.Response(), client, hostport, "", "mount "+prefix)
	}
	if len(d.Routes) == 0 {
		// Nothing declared: the prefix and everything under it, which is what a
		// bare Mount has always registered.
		p := trimPrefix(prefix)
		r.All(p, proxy)
		r.All(p+"/*", proxy)
		return r, nil
	}
	// Declared: exactly the addresses the remote says it answers, so an
	// undeclared path falls through to the host instead of being swallowed.
	for _, rt := range d.Routes {
		if rt.Op == "" {
			r.method(rt.Method, rt.Pattern, []Handler{proxy})
			continue
		}
		r.addRoute(here(1), route{
			method: rt.Method,
			path:   rt.Pattern,
			chain:  []Handler{proxy},
			// The op the remote NAMED, forwarding by name over the same
			// transport the routes proxy over. It is a real registry entry, so
			// an MCP tools/call or a zip.Call on a mounted op reaches the
			// service that owns it rather than finding a hole where a projection
			// stopped at the process boundary.
			op: &registeredOp{
				Method:      rt.Method,
				Path:        rt.Pattern,
				OperationID: rt.Op,
				Origin:      cfg.AppName,
				invoke:      remoteInvoke(client, hostport, rt.Method, rt.Pattern),
			},
		})
	}
	return r, nil
}

// remoteInvoke runs one mounted op by calling the REST route the declaration
// named for it.
//
// Not the remote's by-name call plane, which would have been the tidier
// symmetry: that plane is ZAP end to end, and ZAP encodes STRUCTS. This side
// has no struct — the remote owns the type, which is the entire reason this is
// a mount and not a link — so the only value available here is a free-form map,
// and `zapenc: map[string]interface {} is not a struct` is what asking for the
// symmetry actually gets you.
//
// The declaration already carries the method and the pattern, so the REST route
// is fully addressable from a build input, and JSON is the encoding that can
// carry a value whose shape is not known on this side. It is also the boundary
// encoding by design (HIP-0106: JSON at the edge, ZAP between services) and a
// process boundary crossed without a shared type IS an edge.
//
// The rules it obeys are zip's own, read from the one place each lives: path
// parameters are substituted out of the arguments, and hasBody decides whether
// what remains rides as a body or as a query string.
func remoteInvoke(client Client, host, method, pattern string) func(context.Context, decoder, []byte, map[string]string, map[string]string) (any, error) {
	return func(ctx context.Context, dec decoder, rawIn []byte, query, path map[string]string) (any, error) {
		args := map[string]any{}
		if len(rawIn) > 0 {
			if err := dec(rawIn, &args); err != nil {
				return nil, ErrBadRequest("invalid body: " + err.Error())
			}
		}
		for k, v := range query {
			args[k] = v
		}
		for k, v := range path {
			args[k] = v
		}

		// The URL is the addressing authority, so a path parameter comes OUT of
		// the arguments and goes INTO the path — sending it in both places would
		// let the body contradict the address.
		url := pattern
		for _, name := range colonParams(pattern) {
			v, ok := args[name]
			if !ok {
				return nil, ErrBadRequest("missing path parameter: " + name)
			}
			url = strings.ReplaceAll(url, ":"+name, urlEscape(toStr(v)))
			delete(args, name)
		}

		var body []byte
		if len(args) > 0 {
			b, err := jsonMarshal(args)
			if err != nil {
				return nil, ErrBadRequest("zip: encode " + pattern + ": " + err.Error())
			}
			if hasBody(method) {
				body = b
			} else if q, qerr := queryOf(b); qerr == nil && q != "" {
				url += "?" + q
			}
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(url)
		req.SetHost(host)
		req.Header.SetMethod(method)
		if len(body) > 0 {
			req.SetBody(body)
			req.Header.SetContentType(fiber.MIMEApplicationJSON)
		}
		forwardIdentity(ctx, req)

		if err := client.Do(req, resp); err != nil {
			return nil, Errorf(502, "zip: call %s %s at %s: %v", method, url, host, err)
		}
		switch code := resp.StatusCode(); {
		case code == fasthttp.StatusNoContent:
			return nil, nil
		case code < 200 || code > 299:
			return nil, remoteError(code, method+" "+url, resp.Body())
		}
		if len(resp.Body()) == 0 {
			return nil, nil
		}
		// The bytes the remote wrote, handed on unchanged: re-decoding into a map
		// and re-encoding would round-trip every number through float64 and lose
		// the shape the remote chose.
		return json.RawMessage(append([]byte(nil), resp.Body()...)), nil
	}
}

// toStr renders one decoded argument for a URL. Only scalars address a
// resource; anything else is a caller error the remote will reject with its own
// message, which is more useful than a guess made here.
func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// trimPrefix is the prefix as the router spells it: "/x/" and "/x" are one
// address, and "" is the root.
func trimPrefix(p string) string {
	return strings.TrimSuffix(normPath(p), "/")
}
