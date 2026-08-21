package zip

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	fiber "github.com/zap-proto/fiber/v3"
)

// A plugin DECLARES the paths it answers; a host DISCOVERS them.
//
// A host that hand-maintains a routing row for a repository it does not build
// will get that row wrong, and a wrong row is an outage: cloud's `analytics`
// row listed the read endpoints and omitted the four ingestion doors the app
// actually served, so every product beacon in the fleet answered 405 eighteen
// seconds after the row became load-bearing.
//
// The declaration is projected from the LIVE ROUTER. Not from the AST — that
// cannot cross a repository boundary. Not from the OpenAPI document — a route
// with no typed op is in no document and still needs routing, which is exactly
// the ingestion case. Not from a committed golden — a golden is what gets
// consulted instead of the router.

// PluginPath is where a running plugin serves its own declaration, so a host
// that already started a child can compare what it MOUNTED against what the
// child SERVES. It is one of zip's control-plane routes, and a Declaration
// never includes one (see [App.control]).
const PluginPath = "/.well-known/zip/plugin.json"

// control registers one of zip's OWN routes — a projection of the app rather
// than a door the app's owner wrote — and records it so [App.Declaration]
// excludes it.
//
// Recording is not a filter on a path prefix, because zip's control plane is
// not all under one: /.well-known/openapi.json and /.well-known/zip/op/ are,
// but /docs and the MCP path (default /mcp, configurable) are not. A prefix
// filter therefore let a child DECLARE /docs and /mcp — paths that are not
// under /v1/ at all, that every other plugin also serves, and whose first
// claimant would take the HOST's own document and agent door for the whole
// composition. Registering through here makes leaking one impossible rather
// than unlikely.
func (a *App) control(method, path string, h fiber.Handler) {
	if a.controls == nil {
		a.controls = map[string]bool{}
	}
	a.controls[method+" "+path] = true
	a.buildMu.Lock()
	a.ctl = append(a.ctl, route{method: method, path: path, serve: h})
	// The build moved, so any draft taken before this line is stale. Control
	// routes are not entries, so nothing else bumps the counter for them.
	version.Add(1)
	live := a.live.Load() != nil
	a.buildMu.Unlock()
	if live {
		// A projection route added after something is serving still goes through
		// a generation: there is no other way for the live router to change.
		_ = a.transact(here(1), func() {})
	}
}

// Declaration is what a plugin tells a host: who it is, whether it must be
// running before the first request arrives, every route pattern its router
// holds, and every op name it answers on the call plane.
//
// It is the whole of what a host needs in order to route to a repository it
// does not build. There is no Prefixes field and no Remainder flag: a plugin
// that owns a version remainder declares the catch-all route it actually
// registered ("/v1/*"), so the fact lives in one place — the router.
type Declaration struct {
	Name   string   `json:"name"`
	Eager  bool     `json:"eager,omitempty"`
	Routes []Route  `json:"routes"`
	Ops    []string `json:"ops,omitempty"`
}

// Route is one pattern in the ROUTER's own spelling — ":id", not "{id}". The
// host mounts what the router matches, so the two must be the same string.
type Route struct {
	Method  string `json:"method"`
	Pattern string `json:"pattern"`

	// Op is the operation id this pattern answers under, empty when the route is
	// untyped. It is what makes a remote [Proxy] able to contribute OPS and
	// not merely addresses: the mounting app is handed the declaration inline and
	// reads the ids out of it, instead of asking the remote what it serves.
	Op string `json:"op,omitempty"`
}

// Declaration projects the live router: every method+pattern the app will
// answer, sorted by (pattern, method) and deduplicated, plus every registered
// op name. It is complete by construction — a route the plugin serves and does
// not publish is still declared.
//
// zip's own control plane is excluded — see [App.control]: those are
// per-process routes a host serves for itself, and a child claiming them would
// take the host's own document, agent door and op plane.
func (a *App) Declaration() Declaration {
	a.prepare()
	d := Declaration{Name: a.cfg.AppName, Eager: a.cfg.Eager, Routes: []Route{}}

	seen := map[string]bool{}
	for _, o := range a.plan() {
		r, ok := o.route()
		if !ok || r.method == "" {
			continue
		}
		if r.undeclared {
			continue
		}
		for _, m := range declaredMethods(r.method) {
			k := m + " " + o.abs(r.path)
			if seen[k] {
				continue
			}
			seen[k] = true
			d.Routes = append(d.Routes, Route{Method: m, Pattern: o.abs(r.path)})
		}
	}
	sort.Slice(d.Routes, func(i, j int) bool {
		if d.Routes[i].Pattern != d.Routes[j].Pattern {
			return d.Routes[i].Pattern < d.Routes[j].Pattern
		}
		return d.Routes[i].Method < d.Routes[j].Method
	})

	ops := map[string]bool{}
	// byAddr is what lets a declaration say which op answers WHICH address, so a
	// host that mounts this service can contribute its ops and not merely its
	// addresses. Without it a remote is a set of paths with no names, and every
	// projection but the router stops at the process boundary.
	byAddr := map[string]string{}
	for _, op := range a.Registry() {
		n := opName(op)
		if n == "" {
			continue
		}
		byAddr[op.Method+" "+op.Path] = n
		if !ops[n] {
			ops[n] = true
			d.Ops = append(d.Ops, n)
		}
	}
	for i := range d.Routes {
		d.Routes[i].Op = byAddr[d.Routes[i].Method+" "+d.Routes[i].Pattern]
	}
	sort.Strings(d.Ops)
	return d
}

// declaredMethods expands one registered method into the methods a host must
// route, and is where the SHADOW DOCTRINE lives.
//
// A declaration is projected from the PROGRAM, not from the materialised router,
// and that is what settles the shadow question rather than a filter settling it.
// fiber registers a HEAD alongside every GET and an OPTIONS shadow for CORS.
// Those are not entries — nothing wrote them — so they never reach a declaration
// in the first place, and they do not need to: they are regenerated wherever the
// GET is registered, including on the host that routes to this service. A shadow
// is a property of registering a GET, not a door anybody declared.
//
// The filter this replaced dropped ALL HEAD and OPTIONS, which silently deleted
// an EXPLICITLY declared HEAD — a real door, and one a host would then answer
// 405 on. That is the analytics-outage class this type exists to prevent, so the
// distinction has to be structural: an entry is a door, a shadow is not an entry.
//
// App.All is the one entry that is genuinely many doors, so it expands here.
// HEAD and OPTIONS are left out of that expansion for the same reason as before:
// under All they are indistinguishable from the shadows, and a host that routes
// the GET gets them regenerated.
func declaredMethods(m string) []string {
	if m != methodAll {
		return []string{m}
	}
	return []string{"GET", "POST", "PUT", "PATCH", "DELETE", "CONNECT", "TRACE"}
}

// installPluginRoute serves this app's own declaration, so a host can read the
// SERVED routes off a started child and compare them to the ones it mounted.
// Called from prepare() alongside installOpenAPIRoutes and installMCP.
func (a *App) installPluginRoute() {
	a.control(fiber.MethodGet, PluginPath, func(fc fiber.Ctx) error {
		fc.Set(fiber.HeaderContentType, mimeJSON)
		return fc.JSON(a.Declaration())
	})
}

// Projection names one document an app can emit from its own op registry. The
// verb on the command line IS the name here, and both write a FILE.
type Projection string

const (
	// OpenAPI is the app's own OpenAPI subset, composed upward into the fleet
	// document. Never carved out of the fleet document by prefix: that makes
	// the fleet the source and the plugin a derivative, which is how a
	// catch-all silently swallows a neighbour's routes.
	OpenAPI Projection = "openapi"

	// Declare is the routing declaration a host discovers (see [Declaration]).
	Declare Projection = "declare"
)

// Described writes the projection this process was asked for on the command
// line and reports whether it did:
//
//	<binary> openapi <file>     the app's OpenAPI subset
//	<binary> declare <file>     the app's routing declaration
//
// A main calls it once, after registering every op and BEFORE opening a store
// or dialing a peer — a projection is a function of the code, so a describe run
// must not need a database:
//
//	if done, err := app.Described(); done {
//		return err
//	}
//	return app.Listen(zip.Addr(":9653"))
//
// It writes a FILE and never stdout. A plugin's own dependencies write to
// stdout at construction — zip.New logs a line, GORM logs queries, sqlite-vec
// prints a warning — and `> file` splices those into the front of the
// document.
//
// One call rather than a Described()/Describe() pair, because argv is read in
// exactly one place and a main cannot forget to forward it.
func (a *App) Described() (bool, error) {
	args := os.Args[1:]
	if len(args) == 0 {
		return false, nil
	}
	var mode Projection
	switch Projection(args[0]) {
	case OpenAPI:
		mode = OpenAPI
	case Declare:
		mode = Declare
	default:
		return false, nil
	}
	if len(args) < 2 || args[1] == "" {
		return true, fmt.Errorf("zip: %s needs a destination file", mode)
	}
	return true, a.project(mode, args[1])
}

// project renders one projection to dest, atomically: a reader that finds the
// file finds a whole document, and a failed render leaves the previous one.
func (a *App) project(mode Projection, dest string) error {
	// Ask whether the program is a program BEFORE writing a file that claims to
	// describe it. An invalid build renders as an EMPTY one — 0 ops, 0 routes —
	// so without this, `app declare` writes {"routes":[]} and exits 0, and a
	// release pipeline ships an empty API document for a broken service.
	if err := a.Build(); err != nil {
		return fmt.Errorf("zip: %s: refusing to write a document for a program that does not build: %w", mode, err)
	}
	var v any
	switch mode {
	case OpenAPI:
		v = a.OpenAPISpec()
	case Declare:
		v = a.Declaration()
	default:
		return fmt.Errorf("zip: unknown projection %q", mode)
	}
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("zip: %s: %w", mode, err)
	}
	body = append(body, '\n')

	if dir := filepath.Dir(dest); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("zip: %s: %w", mode, err)
		}
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("zip: %s: %w", mode, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("zip: %s: %w", mode, err)
	}
	return nil
}

// Undeclared returns a Router whose routes SERVE but do not appear in
// [App.Declaration] — and so in none of the projections built from it: the
// OpenAPI document, the MCP tool list, the CLI commands, the by-name call
// plane.
//
// It is for an address that answers without being part of the contract. A
// retirement is the case it was built for: an address that is GONE answers 410
// and names its successor, for every method, because 410 is a statement about
// the target resource and a caller who sent the wrong verb still needs the
// successor. Published, that is one operation per method per address, most of
// them calls that never existed — a contract listing dead endpoints, which is
// a contract nobody can read.
//
// It is deliberately narrow. An address that DOES something and hides is a
// door nobody can find and nobody reviews, which is why this returns a Router
// rather than taking a path: the routes that use it are grouped, visible in
// one place, and read together.
//
// The fact rides the route ENTRY (see [App.addRoute]), so it survives
// composition — a service composed under a host stays undeclared at its new
// path.
func Undeclared(on OpTarget) Router {
	s := on.OpScope()
	g := s.App.group(here(1), s.Prefix)
	g.wrap = s.Middleware
	g.undeclared = true
	return g
}
