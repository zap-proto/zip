package zip

import (
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	fiber "github.com/zap-proto/fiber/v3"
)

// Composition is ONE verb.
//
// An [App] is not a server. It is a PROGRAM: an ordered list of entries, each
// carrying one of exactly three payloads —
//
//	Handler   middleware declared at this level
//	route     one address this app answers
//	*App      another app INCLUDED BY REFERENCE
//
// — and nothing else. [App.Use] appends the first and the third; the route
// methods append the second. There is no second composition verb, because
// there is no second thing composition can mean.
//
// # Inclusion by reference, not delegation
//
// `parent.Use(child)` does not mount, proxy, embed or delegate. It appends a
// REFERENCE to a definition. At build time one interpreter walks one program;
// the child's entries are read in place, exactly as if they had been written
// there. There is no boundary at run time to cross, so there is nothing for a
// request to hop through: those words are reserved for [Proxy], the one
// operation where another runtime genuinely exists.
//
// The same definition may be referenced from any number of places. Each
// reference is an ENTRY, and the entry — not the definition — carries the call
// site, so one definition included from four places has four call sites and
// four occurrences, each with its own path context.
//
// # Why this replaces four verbs
//
// Listen served here, Mount delegated there, Add composed a registrar and
// a fourth composed an App in process. That verb existed for one reason: the
// op registry was EAGER, so composing two apps meant merging two registries at
// call time, and the only other way in — [AdaptNetHTTP] — type-erased an *App
// into a closure and destroyed five projections with it. Once the registry is
// a lazy PROJECTION over a walk of the tree, composing is appending a
// reference, and the projections fall out of the walk.
//
// # The lint, not the rule
//
// Middleware appended AFTER an *App entry on the same receiver is legal and
// means what it says — the stack a node sees is the stack that preceded it
// (see [App.Use] on snapshot semantics). It is intentional when co-located
// (staged composition) and a latent bug when the two are written far apart, and
// no walk can tell those apart, so it is a lint with a breadcrumb rather than a
// rule at any tier. See [App.Lint].

// callsite is where in the program one entry was written.
//
// Every entry has one, recorded by construction at [App.Use], [App.Group] and
// every route method. It is not decoration: a conflict names both parties'
// sites, a cycle names the sites on the ring, a post-seal mutation names the
// site that sealed and the site that then wrote, and the staged-composition
// lint classifies co-located against cross-scope by comparing them. None of
// that can be retrofitted onto entries that did not record it, which is why it
// is on the envelope rather than on any one payload.
type callsite struct {
	file string
	line int
}

// here records the caller's call site. skip is measured from here's caller, so
// a public verb passes 1 to name ITS caller — the line the programmer wrote.
func here(skip int) callsite {
	_, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return callsite{}
	}
	return callsite{file: file, line: line}
}

// String renders "pkg/file.go:42" — the last path segment plus the file, which
// is enough to find the line and short enough to sit inside a message that
// already names two of them.
func (c callsite) String() string {
	if c.line == 0 {
		return "?"
	}
	dir, file := filepath.Split(c.file)
	return filepath.Base(strings.TrimSuffix(dir, string(filepath.Separator))) + "/" + file + ":" + strconv.Itoa(c.line)
}

func (c callsite) known() bool { return c.line != 0 }

// node is the CLOSED set of payloads an entry can carry. Three types, one
// unexported marker, no third-party implementations — which is what lets the
// walk be one type switch over a set that cannot grow behind its back.
//
// It is deliberately NOT [Component]: a route is a node and is not a Component,
// because routes arrive through the route methods and never through Use.
type node interface{ node() }

func (Handler) node() {}
func (route) node()   {}
func (*App) node()    {}

// route is one address an app answers, plus everything every projection needs
// about it. op is non-nil exactly when the route was declared typed, and IS the
// registry entry — one registration, five projections, no second bookkeeping.
//
// path is the path as WRITTEN, never composed with a prefix: a definition
// referenced under two prefixes answers at two absolute paths, so the absolute
// path is a property of the occurrence and is computed by the walk.
type route struct {
	method string
	path   string
	// chain is the registration chain as WRITTEN — zip Handlers, unbound.
	// Binding a Handler to an App is a BUILD step, not a registration step,
	// because the same definition can be built into more than one server and a
	// handler bound to the definition would hand every request a *Ctx belonging
	// to an app that is not serving it.
	chain []Handler
	// serve is the alternative for handlers that are already app-independent:
	// a typed op's core (which reads the request and never materialises a *Ctx)
	// and zip's own control routes. Exactly one of chain/serve is set.
	serve fiber.Handler
	op    *registeredOp
	// oauth is this address speaking RFC 6749: its refusals are {error,
	// error_description} and not problem documents. It rides the ENTRY rather
	// than a table of addresses because composition moves paths — see [OAuth].
	oauth bool
}

// entry is one element of an App's program: a payload, and where it was
// written. The envelope carries the call site so that per-reference metadata
// lives on the reference and never on the definition — which is the whole
// reason one *App included from N places yields N distinguishable occurrences
// without a named reference type.
type entry struct {
	n    node
	site callsite
}

// version counts every append across the process. An App materialises its
// router at a version and rebuilds when the program has moved on, so
// inspecting a half-built app never observes a stale router. It is monotonic
// and process-wide because an append to a CHILD changes the parent's meaning,
// and a child does not know its parents.
var version atomic.Uint64

// Component is what [App.Use] accepts: middleware, or another App.
//
// The set is closed — a Component is a [Handler] or an [*App], and nothing
// else can implement it from outside this package. Two of the three node kinds
// arrive this way; a route arrives through a route method, which is why node
// and Component are different types rather than one type doing two jobs.
type Component interface{ component() }

func (Handler) component() {}
func (*App) component()    {}

// H adapts a bare closure to a [Component].
//
// It exists because Go will not implicitly convert `func(c *zip.Ctx) error` to
// an interface that only the named type [Handler] implements. Anything already
// typed as a Handler — every constructor in the middleware package, every
// variable declared as zip.Handler — needs no wrapper, and neither does an
// inline closure passed to a ROUTE method, because Get/Post/Put/Delete/All
// still take ...Handler. The one construct that needs H is a bare closure
// written inline at a Use call:
//
//	app.Use(middleware.Recover())              // already a Handler
//	app.Get("/x", func(c *zip.Ctx) error {…})  // route method, still ...Handler
//	app.Use(zip.H(func(c *zip.Ctx) error {…})) // bare closure at Use
func H(h Handler) Component { return h }

// Terminal marks h as a handler that ANSWERS an address, and returns it.
//
// A [Handler] may never terminate a request — that is the rule the three node
// kinds encode, and the reason Use takes handlers that WRAP while an address is
// something only a route method can say. Enforcing it needs terminality to be a
// property the walk can READ, and a func value carries nothing a walk can read:
// two closures are the same type, and matching on the name of the constructor
// that built one would bless zip's own leaves and condemn nobody else's.
//
// So the constructor declares it, once, at the only place that knows what it
// built. Every leaf constructor in this module goes through here —
//
//	func Static(fsys fs.FS, opts ...StaticOption) Handler {
//	    return Terminal("zip.Static", func(c *Ctx) error { … })
//	}
//
// — and so can a leaf constructor anywhere else, which is why this is exported.
// The [structural] check then refuses any of them in Use position, naming what
// and where. The handler comes back UNCHANGED and unwrapped: marking costs a
// leaf nothing on the served path, and `app.Get("/assets/*", zip.Static(assets))`
// is the same registration it always was.
//
// what names the constructor for the diagnostic ("zip.Static"), because a
// refusal that says only "a terminal handler" leaves the reader to guess which
// argument on the line it means.
func Terminal(what string, h Handler) Handler {
	if h == nil {
		// A nil func has code pointer 0, and recording THAT would make every
		// nil Handler in the process terminal. Nothing to mark, so mark nothing.
		return nil
	}
	terminals.Store(codeOf(h), what)
	return h
}

// terminalOf reports whether h was declared terminal, and by which constructor.
func terminalOf(h Handler) (string, bool) {
	what, ok := terminals.Load(codeOf(h))
	if !ok {
		return "", false
	}
	return what.(string), true
}

// terminals maps a terminal handler's CODE to the constructor that declared it.
//
// The identity is the closure's code pointer, because a func value is the only
// thing there is to key on and every Handler a given constructor returns runs
// that constructor's one closure body. Go emits a distinct function symbol per
// closure literal and folds no two of them together, so this is an identity and
// not a heuristic — zip.AdaptNetHTTP and zip.AdaptNetHTTPMiddleware return
// closures with the SAME body from the same file and are distinguished
// correctly, which is what TestUse_TerminalityIsADeclaredPropertyNotAName pins.
// One entry covers every instance a constructor will ever hand out, including
// ones built long after this map was last read.
//
// A sync.Map because a constructor runs wherever a program is assembled while a
// build may be reducing a walk on another goroutine. It is read once per
// middleware occurrence at build time and never on the served path.
var terminals sync.Map // uintptr -> string

func codeOf(h Handler) uintptr { return reflect.ValueOf(h).Pointer() }

// Use appends components to this app's program, in order.
//
// # Snapshot semantics
//
// A node's middleware environment is the stack inherited at its INCLUSION SITE
// plus the middleware entries preceding it at its own level. Two clauses,
// which together are what "lexical" means here:
//
//	(a) parent-level entries written AFTER an inclusion site do not reach that
//	    subtree;
//	(b) entries written INSIDE a subtree, whenever they are written, inherit the
//	    environment anchored at the inclusion site.
//
// The subtree's CONTENTS may grow until seal; its ENVIRONMENT may not. So
// staged composition says exactly what it looks like:
//
//	app.Use(public)
//	app.Use(publicAPI)   // sees public
//	app.Use(auth)
//	app.Use(privateAPI)  // sees public + auth
//
// and a late registration inside a group is still a group registration:
//
//	v1 := app.Group("/v1")
//	app.Use(auth)        // parent-level, after inclusion: does NOT reach v1
//	v1.Get("/x", h)      // subtree-internal, late: sees v1's anchored stack
//
// This DIVERGES from Fiber, where the stack is decided by registration time
// against a single flat list. Under Fiber the second example gates /v1/x;
// here it does not. The divergence is the point: it makes the environment a
// function of WHERE a thing is written rather than WHEN, so adding a line to a
// wiring file cannot silently change the auth seam of a subtree written
// elsewhere.
//
// Use returns the App so registrations chain.
func (a *App) Use(cs ...Component) Router {
	site := here(1)
	for _, c := range cs {
		switch v := c.(type) {
		case nil:
			// A nil component is a composition root that decided not to compose
			// anything, which is a legitimate answer.
		case Handler:
			if v == nil {
				continue
			}
			a.appendEntry(entry{n: v, site: site})
		case *App:
			if v == nil {
				panic(fmt.Sprintf("zip: %s: Use(nil *App)", site))
			}
			a.appendEntry(entry{n: v, site: site})
			// A composition owns its child's teardown, so composing cannot leak
			// the child's store. Shutdown is once-guarded, so a definition
			// included twice is still torn down once.
			a.OnShutdown(v.ShutdownWithContext)
		default:
			panic(fmt.Sprintf("zip: %s: %T is not a Component", site, c))
		}
	}
	return a
}

// Group returns a NEW App, included in a at prefix.
//
// A group is not a third kind of thing. It is an App with a prefix, referenced
// from the parent's program like any other — which is why "a scope" and "a
// sub-application" are one mechanism here and two in every framework that grew
// them separately. Middleware handed to Group, or added to the returned App
// later, is scoped to it (see [App.Use] on snapshot semantics).
//
// One definition at two prefixes is two Group calls, so the prefix is visible
// at each inclusion site rather than hidden inside a reference type:
//
//	app.Group("/v1").Use(billing)
//	app.Group("/admin").Use(billing)
//
// The return type is [Router], not *App, even though what comes back IS an
// *App. Go has no return-type covariance, so a concrete return here forces
// EVERY implementor to hand back an *App — and a decorator's group must stay
// decorated, which means returning itself around the group, not the bare group.
// v1.18 had this right; narrowing it in v1.19 is what made hanzoai/commerce's
// mintRouter and hanzoai/cloud's scope unimplementable, the same way
// `Fiber() *fiber.App` did. An abstraction that only its own package can
// implement is not one.
func (a *App) Group(prefix string, handlers ...Handler) Router {
	return a.group(here(1), prefix, handlers...)
}

// group is the concrete constructor. Callers INSIDE the package that need the
// *App itself — wrapRouter.Group, which has to reach g.wrap to carry a scoped
// With down — use this and skip the interface round-trip, so widening the
// exported signature costs nothing internally and no assertion can silently
// drop the chain.
func (a *App) group(site callsite, prefix string, handlers ...Handler) *App {
	g := newApp(a.groupConfig())
	g.prefix = cleanPrefix(prefix)
	// A scoped With wraps every leaf in its scope, and a nested group is still
	// in the scope. Dropping it here reopened a gate that origin/main held shut
	// (wrapRouter.Group used to return another wrapRouter, so the chain rode
	// down every level) — an auth seam quietly lost at the second nesting.
	g.wrap = a.wrap
	// And for the same reason the error vocabulary: a group of an [OAuth] router
	// is still inside the RFC 6749 family, and a nested scope that answered its
	// parent's neighbours' shape would be the same silently-lost property.
	g.oauth = a.oauth
	for _, h := range handlers {
		if h == nil {
			continue
		}
		g.appendEntry(entry{n: h, site: site})
	}
	a.appendEntry(entry{n: g, site: site})
	return g
}

// groupConfig is the parent's config with everything that belongs to a
// DEPLOYMENT stripped out. A group inherits how bytes are handled (error
// handler, body limit, buffers) because those describe the same server; it
// inherits neither the name nor the projections, because a group is not an app
// anyone deploys and must not claim a second document, MCP door or identity.
func (a *App) groupConfig() Config {
	cfg := a.cfg
	cfg.AppName = ""
	cfg.Eager = false
	cfg.OpenAPI = OpenAPIConfig{Disabled: true}
	cfg.MCP = MCPConfig{Disabled: true}
	return cfg
}

// cleanPrefix normalises a group prefix to "" or "/x" — never "/" and never a
// trailing slash, so composing prefixes is concatenation and "/v1" and "/v1/"
// are one scope rather than two.
func cleanPrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if p[0] != '/' {
		p = "/" + p
	}
	return strings.TrimSuffix(p, "/")
}

// appendEntry is the ONE place an App's program grows, so it is the one place
// the seal is enforced and the one place the version moves.
func (a *App) appendEntry(e entry) {
	// buildMu, because a transaction rewrites this very slice while holding it,
	// and both the check and the append must see one consistent program.
	//
	// The freeze is UNCONDITIONAL. There used to be an exemption while a
	// transaction was in flight, and it was a hole rather than a feature: no
	// transaction path reaches this function — compose, Drop and includeRoutes
	// all append directly, already holding the lock — so the exemption could
	// only ever admit a Use racing a transaction from another goroutine. That
	// append landed on the program and materialised on the NEXT, unrelated
	// Include, so a plugin load in one subsystem could activate a route appended
	// hours earlier by a caller that believed it had failed. Locking made the
	// phantom reliable rather than absent; removing the exemption removes it.
	a.buildMu.Lock()
	defer a.buildMu.Unlock()
	if a.frozen.Load() {
		panic(fmt.Sprintf("zip: %s: %s was frozen at %s — it appears in a built generation, so this "+
			"write would change what a running server already published; compose before serving, "+
			"or go through App.Include, which validates the whole program and swaps only if it is "+
			"valid (App.Drop to remove)",
			e.site, a.who(), a.freezeSite))
	}
	a.entries = append(a.entries, e)
	version.Add(1)
}

// who names this app for a diagnostic: its AppName if it has one, else the
// prefix that identifies the group, else that it is anonymous.
func (a *App) who() string {
	switch {
	case a.cfg.AppName != "":
		return strconv.Quote(a.cfg.AppName)
	case a.prefix != "":
		return "the group " + strconv.Quote(a.prefix)
	default:
		return "an anonymous App"
	}
}

// label is who() without the quoting, for a breadcrumb where every element is
// already separated by an arrow.
func (a *App) label() string {
	switch {
	case a.cfg.AppName != "":
		return a.cfg.AppName
	case a.prefix != "":
		return a.prefix
	default:
		return "(anonymous)"
	}
}
