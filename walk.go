package zip

import (
	"errors"
	"fmt"
	"strings"
)

// The walk is the contract.
//
// ONE function flattens a composition into an ordered slice of OCCURRENCES —
// one event per entry visit, in preorder, with the full path context — and
// every projection and every diagnostic in this package is a REDUCER over that
// slice. The router, the OpenAPI document, the MCP tool list, the CLI commands,
// the by-name call plane, the conflict report and the staged-composition lint
// all read the same slice. None of them walks the tree itself; none of them
// keeps traversal logic of its own.
//
// That is the entire reason this design has one composition verb and the old
// one had five. Graft existed because the op registry was computed EAGERLY, at
// compose time, so composing two apps meant merging two registries and every
// projection needed its own merge rule. A projection over a walk needs no merge
// rule at all.
//
// Properties the reducers are entitled to rely on:
//
//   - DEFINITION IDENTITY IS POINTER IDENTITY. Sealing makes a definition
//     immutable, so a pointer can never come to name different content, so
//     nothing needs a synthetic id. It is load-bearing only for *App and for
//     ops; nothing dedupes middleware, which is also why [Handler] being a
//     non-comparable func type costs nothing.
//   - PURE. No global state, no projection-specific logic, no I/O. In
//     particular a remote [Proxy] contributes its declaration INLINE and is
//     never asked over the network what it serves: a walk that did I/O would
//     make the document fallible, slow, untestable, and a function of whether
//     some other process happened to be up at boot.
//   - EAGER, NOT LAZY. Materialised once at seal under sync.Once; before seal
//     every inspection recomputes. Cycle and conflict detection therefore
//     complete BEFORE any reducer runs, so no reducer handles errors mid-loop.
//   - DETERMINISTIC. Same program, identical slice, identical output.
//   - APPEND-STABLE. Appending an entry cannot reorder occurrences inside
//     previously composed subtrees: compose A then B then C and the order
//     within A and within B is untouched. This is what keeps a generated SDK,
//     an OpenAPI document and a CLI diff-stable as a program grows. It is an
//     ORDER guarantee only — appending C can still change VALIDITY, because C's
//     patterns may collide with A's and fail the build.
//
// Pre-seal inspection is explicitly NOT goroutine-safe and does not try to be.
// A program under construction has one writer by definition; adding a mutex
// would buy nothing and would put a lock on the served path.

// occurrence is the walk's internal flattened result — ONE per visit — and it
// is unexported and non-semantic on purpose.
//
// It is not a public model and not a stored source of truth: the TREE remains
// canonical, and this is what one traversal of it produced. The materialised
// form is deliberate. The alternatives are rewalking once per reducer, buffering
// secretly (the slice exists but the document lies), or rendering during
// traversal — which weakens validation-before-rendering, the one ordering every
// other guarantee rests on. At real scale, a thousand-odd routes, the slice is
// free.
//
// What died is its claim to be part of the language: zip exports no IR — no
// Occurrence type, no stored model, no cached contract.
type occurrence struct {
	// n is the payload: Handler, route or *App.
	n node
	// ctx is where this visit sits.
	ctx scope
	// site is where the entry was written.
	site callsite
}

// kind is the NORMALISED discriminator downstream code may switch on. Reducers
// switch on this, never on payload types — the only type switch over node lives
// in walk.go, and a package test enforces it.
type kind uint8

const (
	kindMiddleware kind = iota
	kindRoute
	kindApp
)

func (o occurrence) kind() kind {
	switch o.n.(type) {
	case Handler:
		return kindMiddleware
	case route:
		return kindRoute
	default:
		return kindApp
	}
}

// middleware / route / app read the payload once a [kind] has already said
// which one it is. They are accessors and not a second interpreter: each is one
// assertion with no traversal, no ordering and no semantics of its own.
func (o occurrence) middleware() (Handler, bool) { h, ok := o.n.(Handler); return h, ok }
func (o occurrence) route() (route, bool)        { r, ok := o.n.(route); return r, ok }
func (o occurrence) app() (*App, bool)           { a, ok := o.n.(*App); return a, ok }
func (o occurrence) abs(path string) string      { return joinPath(o.ctx.prefix, path) }

// scope is a visit's path context: everything that is a property of WHERE
// rather than of WHAT.
type scope struct {
	// in is the definition whose entry list this visit came from — the app that
	// WROTE the entry, which is never the same question as where it now runs.
	in *App
	// prefix is the concatenated prefix of every App on the ancestor chain.
	prefix string
	// mw is the middleware stack in force — inherited at the inclusion site,
	// plus the middleware entries preceding this one at its own level. It is a
	// PERSISTENT stack: pushing allocates one cell, and descending into an
	// included App copies a pointer rather than a slice, so a definition
	// referenced from N places does not cost N copies of its environment.
	mw *mwStack
	// trail is the composition breadcrumb, likewise persistent.
	trail *trail
	// depth is the number of inclusions between the root and here.
	depth int
	// origin is WHO DECLARED what is visited here: the nearest ancestor that
	// has a name. Empty at the root, so the served app's own ops are exactly
	// what they were before composition existed.
	//
	// It is the nearest NAMED ancestor and not the enclosing definition,
	// because [App.Group] is an App with its name stripped ([App.groupConfig]).
	// Reading the enclosing definition credited a group with declaring what its
	// app declared — and a group has no name, so the answer was "nobody". A
	// grafted app that declares its routes in groups (every real one does)
	// published every type UNQUALIFIED: hanzoai/iam's 95 schemas arrived as
	// bare Application and Role, colliding with the host's own.
	origin string
}

// mwStack is a persistent middleware stack, innermost cell first, shared
// structurally between every occurrence that inherits it.
type mwStack struct {
	h     Handler
	site  callsite
	depth int // the inclusion depth this cell was DECLARED at
	up    *mwStack
	n     int
}

func (s *mwStack) push(h Handler, site callsite, depth int) *mwStack {
	n := 1
	if s != nil {
		n = s.n + 1
	}
	return &mwStack{h: h, site: site, depth: depth, up: s, n: n}
}

func (s *mwStack) len() int {
	if s == nil {
		return 0
	}
	return s.n
}

// included returns the cells declared INSIDE an included definition
// (depth > 0), outermost-first. Those are the ones materialisation composes
// into a route's own chain instead of registering as router middleware — see
// [App.materialise]. Cells at depth 0 belong to the app being served and stay
// router middleware, so a single-app program's behaviour is untouched.
func (s *mwStack) included() []Handler {
	n := 0
	for c := s; c != nil; c = c.up {
		if c.depth > 0 {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	out := make([]Handler, n)
	for c, i := s, n-1; c != nil; c = c.up {
		if c.depth > 0 {
			out[i] = c.h
			i--
		}
	}
	return out
}

// trail is a persistent composition breadcrumb: root → plugin → group.
//
// It carries the definition itself and not only the name it renders under,
// which makes it the ANCESTOR CHAIN as well as the breadcrumb: an occurrence's
// trail names, in pointers, every definition this event sits beneath. That is
// what lets a question about a subtree ("does anything beneath this definition
// answer an address?") be answered by reading the occurrence slice instead of
// walking the tree a second time — see [routesUnder].
type trail struct {
	def   *App
	label string
	site  callsite
	up    *trail
	n     int
}

func (t *trail) push(def *App, label string, site callsite) *trail {
	n := 1
	if t != nil {
		n = t.n + 1
	}
	return &trail{def: def, label: label, site: site, up: t, n: n}
}

// String renders the breadcrumb root-first.
func (t *trail) String() string {
	if t == nil {
		return ""
	}
	parts := make([]string, t.n)
	for i := t.n - 1; t != nil; t = t.up {
		parts[i] = t.label
		i--
	}
	return strings.Join(parts, " → ")
}

// walk flattens a composition and returns the result.
//
// One function, and the ONLY code that inspects entry payloads as
// Handler | route | *App. Depth-first, in entry order, carrying context:
// prefix path, middleware stack (copies share structure), origin trail.
//
// It does not validate beyond what the traversal itself cannot survive — a
// cycle, which has nothing to enumerate past. Structural and derived validation
// are separate stages that consume this result, so both complete with trails
// before any reducer runs.
//
// BINDING REQUIREMENT: every validator and reducer consumes this result. None
// reopens App.entries or recurses the tree, and none switches on payload types —
// they switch on the normalised [kind].
// TestOneInterpreter_NothingOutsideTheWalkReadsAPayloadOrWalksTheTree enforces it
// mechanically, and every site it allow-lists must still be doing the thing it
// was allowed for.
func walk(root *App) ([]occurrence, error) {
	out := make([]occurrence, 0, 32)
	sc := scope{in: root, trail: (*trail)(nil).push(root, nameOr(root, "root"), callsite{})}
	if err := descend(root, sc, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// descend is the one type switch over the closed node set. Every semantic this
// package claims about composition is a readable property of this function
// rather than behaviour smeared across five verbs:
//
//   - middleware accumulates as it is met, so an entry's environment is what
//     preceded it AT ITS LEVEL (snapshot semantics, clause (b));
//   - an included App is visited with the stack as it stood AT THE INCLUSION
//     SITE, so parent entries written later cannot reach it (clause (a));
//   - the prefix composes down the ancestor chain, so one definition under two
//     prefixes answers at two absolute paths;
//   - the ancestor set is the cycle check, so `a.Use(b); b.Use(a)` is an error
//     with a breadcrumb instead of a hang.
func descend(a *App, sc scope, ancestors []*App, out *[]occurrence) error {
	for _, up := range ancestors {
		if up == a {
			return fmt.Errorf("zip: cycle: %s — an App cannot include itself, "+
				"directly or through a chain", sc.trail.String())
		}
	}
	ancestors = append(ancestors, a)

	mw := sc.mw
	for _, e := range a.entries {
		switch n := e.n.(type) {
		case Handler:
			mw = mw.push(n, e.site, sc.depth)
			here := sc
			here.in, here.mw = a, mw
			*out = append(*out, occurrence{n: n, site: e.site, ctx: here})

		case route:
			here := sc
			here.in, here.mw = a, mw
			*out = append(*out, occurrence{n: n, site: e.site, ctx: here})

		case *App:
			// The included app's environment is anchored HERE. Everything the
			// parent appends after this line is invisible to it, and everything
			// the child appends later still lands inside this anchor.
			origin := sc.origin
			if n.cfg.AppName != "" {
				origin = n.cfg.AppName
			}
			inner := scope{
				in:     n,
				prefix: sc.prefix + n.prefix,
				mw:     mw,
				trail:  sc.trail.push(n, n.label(), e.site),
				depth:  sc.depth + 1,
				origin: origin,
			}
			at := inner
			at.in, at.mw = a, mw
			*out = append(*out, occurrence{n: n, site: e.site, ctx: at})
			if err := descend(n, inner, ancestors, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// structural is validation stage ONE: everything decidable from the shape of the
// composition alone — cycles (already caught by the traversal), duplicated
// addresses, MCP tool-name collisions, two open catalogues, middleware with
// nothing beneath it, and a handler that ANSWERS an address composed in Use
// position.
//
// It reports EVERY problem, not the first. Failing fast would mean composing two
// apps that disagree about three addresses takes three builds to learn three
// facts. And the flattened result is the only place with enough context to name
// BOTH parties: it sees the whole set symmetrically, so it can say which two
// definitions claimed an address and where each was written.
func structural(occ []occurrence) error {
	type claim struct {
		by   string
		site callsite
		via  string
	}
	held := make(map[string]map[string]claim)
	tools := map[string]claim{}
	var openBy *App

	type inert struct {
		mw    callsite
		trail string
	}
	mwOnly := map[*App]*inert{}
	var mwOrder []*App
	seenDef := map[*App]bool{}

	// Keyed by the LINE, not the definition: one message per offending Use,
	// however many times the definition holding it is included.
	type useSite struct {
		def *App
		at  callsite
	}
	badUse := map[useSite]bool{}
	var errs []error

	for _, o := range occ {
		switch o.kind() {
		case kindRoute:
			r, _ := o.route()
			method := strings.ToUpper(r.method)
			path := addrKey(o.abs(r.path))
			mine := claim{by: o.ctx.in.label(), site: o.site, via: o.ctx.trail.String()}
			at := held[path]
			if at == nil {
				at = map[string]claim{}
				held[path] = at
			}
			var prev claim
			var dup bool
			if method == methodAll {
				for _, c := range at {
					prev, dup = c, true
					break
				}
			} else if c, ok := at[method]; ok {
				prev, dup = c, true
			} else if c, ok := at[methodAll]; ok {
				prev, dup = c, true
			}
			if dup {
				errs = append(errs, fmt.Errorf("zip: %s %s: declared by %q at %s (via %s) and by %q at %s (via %s)",
					method, path, prev.by, prev.site, prev.via, mine.by, mine.site, mine.via))
				continue
			}
			at[method] = mine

		case kindMiddleware:
			// TERMINALITY IS ASKED FIRST, AND AT EVERY DEPTH.
			//
			// "A Handler may never terminate a request" is the rule the three
			// node kinds encode, and until now nothing enforced it: the inert
			// check below is strictly weaker — it asks whether a definition
			// carrying middleware has routes beneath it — so
			// `app.Use(zip.Static(assets))` passed on any app with a route, and
			// then the static handler ran BEFORE every route in the composition
			// and answered each one with a file, or a 404 where no file existed.
			// The most literal spelling of the failure the whole rule exists to
			// stop, accepted in silence.
			//
			// Use cannot say WHERE a handler answers. That is not a missing
			// feature, it is what Use MEANS: a handler registered there runs for
			// everything beneath it, so a handler that terminates terminates
			// everything. The address belongs on the route method, where it is
			// written down and where the router can resolve it against every
			// other pattern by specificity.
			//
			// Terminality is a PROPERTY and never a name match. A constructor
			// that builds a leaf says so once, at the only place that can know,
			// by returning its handler through [Terminal] — so zip's own leaves
			// and a third party's are refused by the same rule, and renaming a
			// function changes nothing.
			if h, isMw := o.middleware(); isMw {
				if what, terminal := terminalOf(h); terminal {
					k := useSite{o.ctx.in, o.site}
					if !badUse[k] {
						badUse[k] = true
						errs = append(errs, fmt.Errorf("zip: %s composes %s as middleware at %s (via %s) — that handler ANSWERS "+
							"an address, and Use is for handlers that WRAP.\n\tIn Use position it runs before every route beneath "+
							"this definition and terminates the request there, so nothing composed after it is ever reached.\n\t"+
							"Register it at the address it serves: app.Get(\"/assets/*\", h) rather than app.Use(h)",
							o.ctx.in.who(), what, o.site, o.ctx.trail.String()))
					}
				}
			}
			// Depth 0 is the app being SERVED, and its middleware is never
			// inert: materialisation registers it as ROUTER middleware (see
			// [App.materialise]), so it runs for every request the process
			// answers — including ones that match no route at all, which is what
			// 404 logging, CORS preflight and recovery are for. A root that
			// declares a logger and composes its routes from children has
			// nothing wrong with it. The exemption is exactly this: middleware
			// whose coverage does not come from routes beneath it. It is NOT an
			// exemption from the terminal check above, which asks a different
			// question — whether the handler wraps at all — and asks it of the
			// served app too.
			if o.ctx.depth == 0 {
				continue
			}
			if _, ok := mwOnly[o.ctx.in]; !ok {
				mwOnly[o.ctx.in] = &inert{mw: o.site, trail: o.ctx.trail.String()}
				mwOrder = append(mwOrder, o.ctx.in)
			}

		case kindApp:
			def, _ := o.app()
			if seenDef[def] {
				continue
			}
			seenDef[def] = true
			def.plugMu.Lock()
			cat := append([]mcpTool(nil), def.pluginTools...)
			isOpen := def.open
			def.plugMu.Unlock()
			if isOpen != nil {
				if openBy != nil {
					errs = append(errs, fmt.Errorf("zip: two open MCP plugins: %q and %q — a name no "+
						"catalogue claims must have one owner, and two would make it ambiguous",
						openBy.label(), def.label()))
				} else {
					openBy = def
				}
			}
			for _, tl := range cat {
				mine := claim{by: def.label(), via: o.ctx.trail.String()}
				if prev, dup := tools[tl.name]; dup {
					errs = append(errs, fmt.Errorf("zip: MCP tool %q: claimed by %q (via %s) and by %q (via %s) — "+
						"a tools/call resolves by name, so the second claimant is unreachable",
						tl.name, prev.by, prev.via, mine.by, mine.via))
					continue
				}
				tools[tl.name] = mine
			}
		}
	}

	// The SUBTREE, not the definition's own entries. A group's routes usually
	// come from the definitions it includes, and its middleware legitimately
	// wraps those — that is what a scoped Group is for.
	has := routesUnder(occ)
	for _, def := range mwOrder {
		if has[def] {
			continue
		}
		st := mwOnly[def]
		errs = append(errs, fmt.Errorf("zip: %s declares middleware at %s and no routes anywhere beneath it (via %s) — "+
			"a definition's middleware wraps the routes in its OWN subtree, so with none it would "+
			"silently never run.\n\tIf it WRAPS: compose the routes it guards beneath this definition, "+
			"or move it to the app that has them.\n\tIf it ANSWERS an address, register it at that address "+
			"instead: app.Get(\"/assets/*\", h) rather than app.Use(h) — Use cannot say WHERE a handler "+
			"answers, which is why it is only for handlers that wrap",
			def.who(), st.mw, st.trail))
	}
	return errors.Join(errs...)
}

// derived is validation stage TWO: everything decidable only AFTER identities
// are derived from the shape.
//
// It is separate because it cannot run earlier. An operation id is a function of
// the declared id and the absolute path the occurrence answers at, so two
// occurrences can be structurally fine — different paths, no address conflict —
// and still derive the SAME id: one declaration included twice, or two addresses
// the encoding cannot tell apart (/v1/a/b_c and /v1/a/b/c, see [ID]). That
// collision does not exist until derivation happens, and a document with two
// operations under one operationId is invalid.
func derived(occ []occurrence) error {
	type claim struct {
		path string
		via  string
		site callsite
	}
	ids := map[string]claim{}
	var errs []error
	for _, o := range occ {
		r, ok := o.route()
		if !ok || r.op == nil {
			continue
		}
		id := occurrenceID(o.ctx.prefix, r.op)
		mine := claim{path: o.abs(r.path), via: o.ctx.trail.String(), site: o.site}
		if prev, dup := ids[id]; dup {
			// A DECLARED id gets its own sentence, because the fix is different
			// and because this is the case composition used to hide: the id was
			// qualified by each occurrence's prefix, so the collision never
			// surfaced and the author's published name was rewritten instead.
			// See [occurrenceID].
			if r.op.OperationID == id {
				errs = append(errs, fmt.Errorf("zip: operation id %q is declared once and occurs twice: %s (via %s) and %s at %s (via %s) — "+
					"a declared id is a published name (SDK method, operationId, MCP tool, command) and survives composition verbatim, "+
					"so one declaration cannot be two operations.\n\tEither drop WithOperationID and take the address-derived id, which "+
					"names each occurrence by the absolute path it answers at, or include the definition once",
					id, prev.path, prev.via, mine.path, mine.site, mine.via))
				continue
			}
			errs = append(errs, fmt.Errorf("zip: operation id %q is derived twice: %s (via %s) and %s at %s (via %s) — "+
				"an id is a published SDK method name, so two operations cannot share one",
				id, prev.path, prev.via, mine.path, mine.site, mine.via))
			continue
		}
		ids[id] = mine
	}
	return errors.Join(errs...)
}

// verify runs both stages in order over ONE walk. Nothing publishes unless both
// complete clean.
func verify(root *App) error {
	occ, err := walk(root)
	if err != nil {
		return err // a cycle: there is nothing to enumerate past it
	}
	if err := structural(occ); err != nil {
		return err
	}
	return derived(occ)
}

// routesUnder is the set of definitions with a route ANYWHERE beneath them.
//
// One pass over the occurrences, no traversal: a route occurrence's trail IS
// its ancestor chain (see [trail]), so every definition it sits beneath is the
// cells above it. This used to recurse over `def.entries` per definition, which
// made it a SECOND interpreter of the same program — one that needed its own
// cycle guard, could disagree with the walk about what a composition contains,
// and read a definition's live entry list rather than the one the generation
// was built from. Reducing the slice needs no cycle guard at all: a cyclic
// composition never reaches a reducer, because [descend] returns the error and
// [walk] stops.
func routesUnder(occ []occurrence) map[*App]bool {
	has := map[*App]bool{}
	for _, o := range occ {
		if o.kind() != kindRoute {
			continue
		}
		for t := o.ctx.trail; t != nil; t = t.up {
			has[t.def] = true
		}
	}
	return has
}

// addrKey is one path as the conflict check sees it: the router's own spelling,
// with "/x/" and "/x" one address and not two.
func addrKey(pattern string) string {
	p := normPath(pattern)
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// nameOr is an app's name, or fallback when it has none.
func nameOr(a *App, fallback string) string {
	if a.cfg.AppName != "" {
		return a.cfg.AppName
	}
	if a.prefix != "" {
		return a.prefix
	}
	return fallback
}

// occurrenceID is an op's id AT ONE OCCURRENCE, and it asks ONE question first:
// did the author WRITE the id down?
//
// # A declared id is the contract, and composition is not allowed to edit it
//
// [WithOperationID] is a published name. It is the SDK method, the OpenAPI
// operationId, the MCP tool an agent has cached, the CLI command a script
// spells and the key the by-name call plane resolves. An author writing
// `WithOperationID("CreateRole")` has made a promise to every one of those
// surfaces, and that promise cannot be conditional on where a HOST later chose
// to include the definition — a service embedded at /v1/o11y and the same
// service deployed standalone publish the same operations, because they ARE the
// same operations.
//
// Qualifying it anyway is what this function used to do, and the cost was
// exact: including hanzoai/o11y under /v1/o11y renamed all 353 of its published
// ops to v1.o11y.<id> — every tool, every operationId, every command, every
// generated method — as a silent side effect of a wiring line. A declaration
// silently overridden by the shape around it is not a declaration.
//
// So a declared id survives composition VERBATIM.
//
// # An UNDECLARED id is derived from the ABSOLUTE path
//
// Without a declaration there is no promise to keep, and there IS a real
// ambiguity to resolve: a definition included twice produces TWO operations
// from one registration, and an OpenAPI document with two operations under one
// operationId is invalid. So the id is [ID] of the method and the address the
// occurrence actually ANSWERS AT — the prefix joined to the op's own path:
//
//	/v1/billing    + /invoices/:id -> get_v1_billing_invoices_by_id
//	/admin/billing + /invoices/:id -> get_admin_billing_invoices_by_id
//
// Two inclusions have two absolute paths, so this distinguishes them for free,
// and it is the ONLY input: the id is what the address says, so a definition
// reached through a group and the same routes written flat produce the same id.
// They are the same operations at the same addresses; nothing about the wiring
// that led there is part of the name.
//
// The prefix used to be spelled as a DOTTED qualifier instead —
// v1.billing.get_invoices_id — derived from the shape rather than the address.
// Two rules for one id is one rule too many, and these two disagreed: the
// published document said post_v1_agents_targets while the tool list said
// v1.agents.post_targets, so an agent that read the document and asked for the
// tool it named got "no such tool". The document's rule is the published one, so
// it is the rule that stayed.
//
// Never positional. "First occurrence wins" and "append -2" both make the
// generated output a function of MOUNT ORDER, so reordering two Use calls in a
// wiring file becomes a breaking change in every published SDK. An address is
// not an order — it is the same however the wiring arrived at it.
//
// # The case the two rules meet
//
// A definition that DECLARES an id and is included TWICE is now a refused
// program rather than a silent rename: one global name cannot be two
// operations, and the per-occurrence qualifier that used to paper over it was
// the very override this rule exists to stop. [derived] reports it, names both
// occurrences, and says what to do — drop the declaration and take the
// address-derived id, or include the definition once. The author chooses; zip
// does not choose for them behind their back.
//
// An occurrence at the root prefix answers at its own path, so an app that
// composes nothing publishes exactly what it would standalone, byte for byte.
func occurrenceID(prefix string, op *registeredOp) string {
	if op.OperationID != "" {
		return op.OperationID // declared: composition does not get a vote
	}
	return ID(op.Method, joinPath(prefix, op.Path))
}

// A handler that ANSWERS an address is registered at that address; Use is for
// handlers that WRAP. app.Use(static) cannot say WHERE it answers, which is the
// same "structure encodes the semantics" argument that made Group the way to
// express a middleware scope rather than line position.
//
// That rule is now CHECKED rather than merely stated. [structural] refuses a
// terminal handler in Use position — at any depth, including on the app being
// served, which is exactly where a wiring file puts app.Use(zip.Static(assets))
// — and it decides terminality by the property a constructor declares through
// [Terminal], never by the name of the function that built the handler. A name
// match would bless zip's three leaves and miss every leaf anyone else writes,
// which is the wrong half of the fleet.
