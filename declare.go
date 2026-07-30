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
	a.fiber.Add([]string{method}, path, h)
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
	for _, r := range a.fiber.GetRoutes(true) {
		if r.Method == "" || r.Path == "" {
			continue
		}
		if a.controls[r.Method+" "+r.Path] {
			continue
		}
		// fiber registers a HEAD alongside every GET and an OPTIONS shadow for
		// CORS; neither is a door a host routes on its own.
		if r.Method == fiber.MethodHead || r.Method == fiber.MethodOptions {
			continue
		}
		k := r.Method + " " + r.Path
		if seen[k] {
			continue
		}
		seen[k] = true
		d.Routes = append(d.Routes, Route{Method: r.Method, Pattern: r.Path})
	}
	sort.Slice(d.Routes, func(i, j int) bool {
		if d.Routes[i].Pattern != d.Routes[j].Pattern {
			return d.Routes[i].Pattern < d.Routes[j].Pattern
		}
		return d.Routes[i].Method < d.Routes[j].Method
	})

	ops := map[string]bool{}
	for _, op := range a.registry {
		if n := opName(op); n != "" && !ops[n] {
			ops[n] = true
			d.Ops = append(d.Ops, n)
		}
	}
	sort.Strings(d.Ops)
	return d
}

// installPluginRoute serves this app's own declaration, so a host can read the
// SERVED routes off a started child and compare them to the ones it mounted.
// Called from prepare() alongside installOpenAPIRoutes and installMCP.
func (a *App) installPluginRoute() {
	a.control(fiber.MethodGet, PluginPath, func(fc fiber.Ctx) error {
		fc.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
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
