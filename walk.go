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
//     particular a remote [Mount] contributes its declaration INLINE and is
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

// scope is a visit's path context: everything that is a property of WHERE
// rather than of WHAT. It is handed to the callback and never retained.
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
type trail struct {
	label string
	site  callsite
	up    *trail
	n     int
}

func (t *trail) push(label string, site callsite) *trail {
	n := 1
	if t != nil {
		n = t.n + 1
	}
	return &trail{label: label, site: site, up: t, n: n}
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

// walk visits every entry of a composition in flattened preorder, handing the
// callback the node, its path context and where it was written.
//
// There is no payload type and nothing retains the sequence. The callback's
// parameters WERE the payload, so a struct wrapping them was a noun invented to
// carry what three arguments already carry — and retaining the slice made every
// backend a consumer of a materialised intermediate rather than a reducer over
// a traversal.
//
// It does NOT validate. Cycles are detected here because the traversal cannot
// proceed through one, but conflicts are a separate first pass ([validate]), so
// that cycles and conflicts complete — with trails — before any backend sees a
// single visit.
func walk(root *App, visit func(n node, sc scope, site callsite) error) error {
	sc := scope{in: root, trail: (*trail)(nil).push(nameOr(root, "root"), callsite{})}
	return descend(root, sc, nil, visit)
}

// abs is the absolute path a visit answers at: the definition's own path
// composed with the prefix accumulated along the ancestor chain.
func (sc scope) abs(path string) string { return joinPath(sc.prefix, path) }

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
func descend(a *App, sc scope, ancestors []*App, visit func(node, scope, callsite) error) error {
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
			if err := visit(n, scope{in: a, prefix: sc.prefix, mw: mw, trail: sc.trail, depth: sc.depth}, e.site); err != nil {
				return err
			}

		case route:
			if err := visit(n, scope{in: a, prefix: sc.prefix, mw: mw, trail: sc.trail, depth: sc.depth}, e.site); err != nil {
				return err
			}

		case *App:
			// The included app's environment is anchored HERE. Everything the
			// parent appends after this line is invisible to it, and everything
			// the child appends later still lands inside this anchor.
			inner := scope{
				in:     n,
				prefix: sc.prefix + n.prefix,
				mw:     mw,
				trail:  sc.trail.push(n.label(), e.site),
				depth:  sc.depth + 1,
			}
			if err := visit(n, scope{in: a, prefix: inner.prefix, mw: mw, trail: inner.trail, depth: inner.depth}, e.site); err != nil {
				return err
			}
			if err := descend(n, inner, ancestors, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

// verify is the FIRST PASS, and its own traversal.
//
// Cycles and conflicts complete — with trails — before any backend sees a
// single visit. Interleaving validation with a backend's visits would mean a
// router half-built out of a program that turns out not to compose, so the
// separation is the guarantee rather than a tidiness preference.
//
// It reports EVERY problem, not the first. Failing fast would mean composing
// two apps that disagree about three addresses takes three builds to learn
// three facts. And the traversal is the only place with enough context to name
// BOTH parties: it sees the whole set symmetrically, so it can say which two
// definitions claimed an address and where each was written. Eager composition
// could only ever attribute a collision to whichever app composed SECOND, which
// is wiring-file line order and means nothing.
func verify(root *App) error {
	type claim struct {
		by   string
		site callsite
		via  string
	}
	// Addresses, keyed by PATH then method — see the note in this file on why
	// the method needs normalising and why ALL collides with everything.
	held := make(map[string]map[string]claim)

	// MCP tool names: two definitions cannot claim one, because a tools/call
	// resolves by name and the loser is unreachable. At most one OPEN
	// catalogue, for the same reason a name no catalogue claims needs one owner.
	tools := map[string]claim{}
	var openBy *App

	// Middleware with nothing beneath it. Keyed by definition: one verdict per
	// definition, however often it is included.
	type inert struct {
		mw    callsite
		trail string
	}
	mwOnly := map[*App]*inert{}
	var mwOrder []*App

	seenDef := map[*App]bool{}
	var errs []error

	if err := walk(root, func(n node, sc scope, site callsite) error {
		switch v := n.(type) {
		case route:
			method := strings.ToUpper(v.method)
			path := addrKey(sc.abs(v.path))
			mine := claim{by: sc.in.label(), site: site, via: sc.trail.String()}
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
				return nil
			}
			at[method] = mine

		case Handler:
			if sc.depth == 0 {
				return nil // the app being served IS the server; its middleware is global
			}
			if _, ok := mwOnly[sc.in]; !ok {
				mwOnly[sc.in] = &inert{mw: site, trail: sc.trail.String()}
				mwOrder = append(mwOrder, sc.in)
			}

		case *App:
			if seenDef[v] {
				return nil
			}
			seenDef[v] = true
			v.plugMu.Lock()
			cat := append([]mcpTool(nil), v.pluginTools...)
			isOpen := v.open
			v.plugMu.Unlock()
			if isOpen != nil {
				if openBy != nil {
					errs = append(errs, fmt.Errorf("zip: two open MCP plugins: %q and %q — a name no "+
						"catalogue claims must have one owner, and two would make it ambiguous",
						openBy.label(), v.label()))
				} else {
					openBy = v
				}
			}
			for _, tl := range cat {
				mine := claim{by: v.label(), via: sc.trail.String()}
				if prev, dup := tools[tl.name]; dup {
					errs = append(errs, fmt.Errorf("zip: MCP tool %q: claimed by %q (via %s) and by %q (via %s) — "+
						"a tools/call resolves by name, so the second claimant is unreachable",
						tl.name, prev.by, prev.via, mine.by, mine.via))
					continue
				}
				tools[tl.name] = mine
			}
		}
		return nil
	}); err != nil {
		return err // a cycle: there is nothing to enumerate past it
	}

	// A definition's middleware wraps the routes in its OWN subtree. With none
	// there is nothing to wrap, so inert is CORRECT under lexical anchoring —
	// what is unacceptable is that it was silent and reads exactly like working
	// code.
	for _, def := range mwOrder {
		if routesUnder(def, map[*App]bool{}) {
			continue
		}
		s := mwOnly[def]
		errs = append(errs, fmt.Errorf("zip: %s declares middleware at %s and no routes anywhere beneath it (via %s) — "+
			"a definition's middleware wraps the routes in its OWN subtree, so with none it would "+
			"silently never run.\n\tIf it WRAPS: compose the routes it guards beneath this definition, "+
			"or move it to the app that has them.\n\tIf it ANSWERS an address (zip.Static and friends), "+
			"register it at that address instead: app.Get(\"/assets/*\", h) rather than app.Use(h) — "+
			"Use cannot say WHERE a handler answers, which is why it is only for handlers that wrap",
			def.who(), s.mw, s.trail))
	}
	return errors.Join(errs...)
}

// routesUnder reports whether anything in def's subtree answers an address.
func routesUnder(def *App, seen map[*App]bool) bool {
	if seen[def] {
		return false // a cycle is reported elsewhere; do not spin here
	}
	seen[def] = true
	for _, e := range def.entries {
		switch n := e.n.(type) {
		case route:
			return true
		case *App:
			if routesUnder(n, seen) {
				return true
			}
		}
	}
	return false
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

// occurrenceID is an op's id AT ONE OCCURRENCE: the declared id qualified by the
// prefix that occurrence answers under.
//
// This is the landmine the lazy model steps on and the eager one never reached.
// A definition included twice declares ONE id and produces TWO operations, and
// an OpenAPI document with two operations under one operationId is invalid.
// The qualification must therefore be deterministic and derived from the
// composition's SHAPE:
//
//	/v1/billing    + listInvoices -> v1.billing.listInvoices
//	/admin/billing + listInvoices -> admin.billing.listInvoices
//
// Never positional. "First occurrence wins" and "append -2" both make the
// generated output a function of MOUNT ORDER, so reordering two Use calls in a
// wiring file becomes a breaking change in every published SDK. This rule binds
// every downstream artifact — document, tool name, command, call-plane name —
// because they all read the same id.
//
// An occurrence at the root prefix is unqualified, so an app that composes
// nothing publishes exactly the ids it declared, byte for byte.
func occurrenceID(prefix, id string) string {
	d := dotted(prefix)
	if d == "" {
		return id
	}
	return d + "." + id
}

// dotted turns a path prefix into an id qualifier: "/v1/billing" -> "v1.billing".
// Path-parameter and wildcard punctuation is dropped rather than encoded — a
// prefix with a parameter in it names a family of deployments, and an id must
// name one method.
func dotted(prefix string) string {
	var b strings.Builder
	for _, seg := range strings.Split(prefix, "/") {
		seg = strings.Trim(seg, ":*{}")
		if seg == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
				b.WriteRune(r)
			default:
				b.WriteByte('_')
			}
		}
	}
	return b.String()
}

// A handler that ANSWERS an address is registered at that address; Use is for
// handlers that WRAP. app.Use(static) cannot say WHERE it answers, which is the
// same "structure encodes the semantics" argument that made Group the way to
// express a middleware scope rather than line position. This is why the inert
// check above condemns no form anyone writes: every zip.Static in the fleet is
// already registered at an address.
