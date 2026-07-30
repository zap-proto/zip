package zip

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zap-proto/zip/internal/jsonenc"
)

// Module is one loaded extension — the zip-side projection of the HIP-0105
// Module contract. [App.Module] mounts one of these at one route.
type Module interface {
	Name() string
	Runtime() string
	Exports() []string
	Invoke(ctx context.Context, fn string, payload []byte) ([]byte, error)
	Close() error
}

// Loader loads extensions for [App.Module]. It is the zip-side projection of
// the HIP-0105 extension runtime contract and it lives HERE, at the root,
// because the root is what consumes it: [Config.Loader] holds one and
// [App.Module] calls it.
//
// The interface is duck-typed and zip does NOT import an implementation —
// so a consumer that never evaluates JavaScript never compiles one. Build your
// loader and pass it in:
//
//	app := zip.New(zip.Config{Loader: myLoader})
//
// Duck-typed here means structural, not loose: LoadDir must return
// map[string][Module] EXACTLY. A backend that declares its own Module type
// (hanzoai/base/plugins/extruntime does) therefore satisfies this through a
// thin adapter that returns [Module], not by direct assignment — Go requires
// identical result types, and an equivalent method set is not enough.
//
// The in-tree JavaScript implementation is package
// [github.com/zap-proto/zip/js] — goja to evaluate and esbuild to
// bundle. It is a SEPARATE import on purpose: that cost belongs to the
// binaries that ask for JS, not to every consumer of zip.
type Loader interface {
	// LoadDir scans a directory for extension manifests and returns
	// loaded modules keyed by manifest name.
	LoadDir(ctx context.Context, dir string) (map[string]Module, error)

	// LoadOne loads a single extension by directory. zip uses this for
	// [App.Module], which mounts ONE extension at one route. Implementers
	// may implement it by calling LoadDir on the parent and selecting
	// the result.
	LoadOne(ctx context.Context, dir string) (Module, error)

	// Runtimes returns the registered runtime names ("goja", "wazero",
	// "v8go", "pyvm", "starlark", "native").
	Runtimes() []string
}

// moduleEnvelope is the JSON shape every extension runtime receives.
// Same shape across wasm / goja / pyvm / starlark — the host serializes
// once and the guest sees the same bytes regardless of which engine ran
// it. RawMessage is from encoding/json (v1) because the type lives in
// v1 only; both v1 and v2 (encoding/json/v2) honor its MarshalJSON /
// UnmarshalJSON so the envelope serializes identically under either
// build of jsonenc.
type moduleEnvelope struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Params  map[string]string `json:"params"`
	Query   map[string]string `json:"query"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body,omitempty"`

	// Hanzo identity — populated from gateway X-* headers per HIP-0026.
	Org       string `json:"org,omitempty"`
	User      string `json:"user,omitempty"`
	UserEmail string `json:"userEmail,omitempty"`
}

// moduleResponse is the JSON shape every extension runtime returns.
type moduleResponse struct {
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// Module mounts a single HIP-0105 extension at the given method+path — the
// one way to put an extension on the app. The `methodPath` form is
// "METHOD /path" (e.g. "POST /v1/validate"), matching the
// idiom. `runtime` selects the backing engine ("wasm" | "goja" | "pyvm" |
// "starlark" | "v8go" | "native"); `modulePath` is the directory containing
// the extension.json manifest.
//
//	app.Module("POST /v1/policy/eval", "wasm", "./extensions/policy")
//	app.Module("POST /v1/transform",   "pyvm", "./extensions/transform")
//	app.Module("POST /v1/webhook",     "goja", "./extensions/webhook")
//
// The extension's exported function name is inferred from the path's last
// non-{param} segment, lowercased (e.g. "POST /v1/policy/eval" → "eval").
//
// # A module route registers a ROUTE and no op, and cannot register one
//
// So it is invisible to every projection: it is in no OpenAPI document, is no
// MCP tool, and no service can reach it with zip.Call. That is a real hole —
// the envelope in and out of a module IS a JSON contract — and it is stated
// here rather than left to be discovered, because the two reasons it cannot
// close today are both structural:
//
//  1. THERE IS NO TYPE TO DERIVE A SCHEMA FROM. A typed op's schema comes from
//     its In/Out Go types. A module is LOADED at run time out of a directory —
//     that is the whole point of an extension — so at the moment this route is
//     registered zip holds a [Module] (Name/Runtime/Exports/Invoke) and no
//     declaration of what the module accepts or returns. Registering it as an
//     open object would put the path in the document while saying nothing
//     about the contract, which is not the same thing as documenting it.
//  2. A TYPED OP CANNOT EXPRESS THE MODULE'S RESPONSE. A module answers with a
//     moduleResponse — status, headers, body — and modules use it (a redirect,
//     a 404, a non-JSON body). A typed op returns Out and zip decides the
//     status, so routing this through registerTyped would silently drop a
//     capability the envelope exists to provide.
//
// What closes it is (1): an extension that DECLARES its contract, via a
// manifest schema surfaced on [Module]. The op would then carry the module's
// own schema and every projection follows, with the route keeping the envelope
// it has now. Until an extension declares one there is nothing to project, and
// inventing a shape for it here would be a second source of truth for a
// contract zip does not own.
//
// A route whose contract IS known belongs in a typed op — declare it with
// [Get]/[Post] and let the module be an implementation detail behind it.
func (a *App) Module(methodPath, runtimeName, modulePath string) error {
	method, path, fn, err := parseModuleSpec(methodPath)
	if err != nil {
		return err
	}
	if a.loader == nil {
		return fmt.Errorf("zip: app.Module requires Config.Loader to be set")
	}
	if a.cfg.AllowedRuntimes != nil {
		ok := false
		for _, r := range a.cfg.AllowedRuntimes {
			if r == runtimeName {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("zip: runtime %q not in AllowedRuntimes", runtimeName)
		}
	}

	mod, err := a.loader.LoadOne(nil, filepath.Clean(modulePath))
	if err != nil {
		return fmt.Errorf("zip: load module %s: %w", modulePath, err)
	}
	if got := mod.Runtime(); got != runtimeName {
		// Loader picked a different runtime than caller asked — likely a
		// manifest mismatch. Refuse to mount.
		_ = mod.Close()
		return fmt.Errorf("zip: module %s declared runtime=%q, caller passed %q", modulePath, got, runtimeName)
	}

	a.logger.Info("zip mounting module",
		"method", method, "path", path, "fn", fn,
		"runtime", runtimeName, "modulePath", modulePath)

	handler := func(c *Ctx) error {
		env := buildEnvelope(c)
		payload, err := jsonenc.Marshal(env)
		if err != nil {
			return ErrInternal("marshal envelope: " + err.Error())
		}
		out, err := mod.Invoke(c.Context(), fn, payload)
		if err != nil {
			return ErrInternal("module invoke: " + err.Error())
		}
		var resp moduleResponse
		if len(out) > 0 {
			if err := jsonenc.Unmarshal(out, &resp); err != nil {
				// Treat bare JSON output as the body with status 200.
				return c.Bytes(200, out)
			}
		}
		if resp.Status == 0 {
			resp.Status = 200
		}
		for k, v := range resp.Headers {
			c.SetHeader(k, v)
		}
		if len(resp.Body) == 0 {
			return c.NoContent(resp.Status)
		}
		// Default to application/json for module responses when no
		// Content-Type was set by the module. Modules can override via
		// the response envelope's headers map.
		if resp.Headers["Content-Type"] == "" && resp.Headers["content-type"] == "" {
			c.SetHeader("Content-Type", "application/json; charset=utf-8")
		}
		return c.Bytes(resp.Status, resp.Body)
	}
	a.method(method, path, []Handler{handler})

	// Release the module's resources on shutdown. Runs as a teardown hook,
	// i.e. AFTER in-flight requests drain, so a module stays live for any
	// request still executing it.
	a.OnShutdown(func(context.Context) error { return mod.Close() })
	return nil
}

// parseModuleSpec parses "METHOD /path" into (method, path, fnName).
// fnName defaults to the last non-{param} path segment, lowercased.
//
//	"POST /v1/validate"            -> POST /v1/validate validate
//	"GET  /v1/users/{id}"          -> GET  /v1/users/{id} users
//	"POST /v1/foo/bar/baz"         -> POST /v1/foo/bar/baz baz
func parseModuleSpec(spec string) (method, path, fn string, err error) {
	parts := strings.Fields(spec)
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("zip: module spec %q: want \"METHOD /path\"", spec)
	}
	method = strings.ToUpper(parts[0])
	path = parts[1]
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if s != "" && !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, ":") {
			fn = strings.ToLower(s)
			break
		}
	}
	if fn == "" {
		fn = "handler"
	}
	return method, path, fn, nil
}

func buildEnvelope(c *Ctx) moduleEnvelope {
	headers := map[string]string{}
	c.fc.Request().Header.VisitAll(func(k, v []byte) {
		headers[string(k)] = string(v)
	})

	queries := map[string]string{}
	c.fc.Request().URI().QueryArgs().VisitAll(func(k, v []byte) {
		queries[string(k)] = string(v)
	})

	// Fiber v3 doesn't expose AllParams directly — common pattern is to
	// pull known params. For the envelope shape we carry the body and let
	// the guest re-parse path params from the URL if it needs them.
	return moduleEnvelope{
		Method:    c.Method(),
		Path:      c.Path(),
		Params:    map[string]string{},
		Query:     queries,
		Headers:   headers,
		Body:      c.Body(),
		Org:       c.Org(),
		User:      c.User(),
		UserEmail: c.UserEmail(),
	}
}
