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
//     particular a remote [App.Mount] contributes its declaration INLINE and is
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

// occurrence is ONE event: one entry, of one definition, visited in one place.
//
// The definition is shared; the occurrence is not. A definition included from
// two places yields two occurrences of every entry it holds, each with its own
// prefix, middleware stack and breadcrumb — which is exactly why the projections
// can key SURFACE on the occurrence and TYPES on the definition without either
// choice leaking into the walk.
type occurrence struct {
	// n is the payload: [Handler], [route] or [*App]. The three kinds.
	n node
	// in is the definition whose entry list this event came from — the app that
	// WROTE the entry, which is never the same question as where it now runs.
	in *App
	// site is where the entry was written.
	site callsite
	// ctx is where this event sits in the composition.
	ctx scope
}

// isMiddleware / isRoute / isApp read the kind. A type switch is the normal
// way; these exist for reducers that want one kind and no switch.
func (o occurrence) route() (route, bool) { r, ok := o.n.(route); return r, ok }
func (o occurrence) app() (*App, bool)    { a, ok := o.n.(*App); return a, ok }

// abs is the absolute path this occurrence answers at: the definition's own
// path composed with the prefix accumulated along the ancestor chain.
func (o occurrence) abs(path string) string { return joinPath(o.ctx.prefix, path) }

// scope is an occurrence's path context: everything that is a property of WHERE
// rather than of WHAT.
type scope struct {
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

// walk flattens a composition into its occurrences and validates it.
//
// It returns the occurrences AND the error, always both: a conflict does not
// stop enumeration (every projection is still computable, and refusing to
// enumerate would hide the other conflicts), while a cycle does, because there
// is nothing to enumerate past it.
func walk(root *App) ([]occurrence, error) {
	out := make([]occurrence, 0, 16)
	sc := scope{trail: (*trail)(nil).push(nameOr(root, "root"), callsite{})}
	if err := descend(root, sc, nil, &out); err != nil {
		return out, err
	}
	return out, conflicts(out)
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
			*out = append(*out, occurrence{n: n, in: a, site: e.site,
				ctx: scope{prefix: sc.prefix, mw: mw, trail: sc.trail, depth: sc.depth}})

		case route:
			*out = append(*out, occurrence{n: n, in: a, site: e.site,
				ctx: scope{prefix: sc.prefix, mw: mw, trail: sc.trail, depth: sc.depth}})

		case *App:
			// The included app's environment is anchored HERE. Everything the
			// parent appends after this line is invisible to it, and everything
			// the child appends later still lands inside this anchor.
			inner := scope{
				prefix: sc.prefix + n.prefix,
				mw:     mw,
				trail:  sc.trail.push(n.label(), e.site),
				depth:  sc.depth + 1,
			}
			*out = append(*out, occurrence{n: n, in: a, site: e.site, ctx: inner})
			if err := descend(n, inner, ancestors, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// conflicts reports EVERY duplicated address, not the first.
//
// Failing fast would report one collision per build, so composing two apps that
// disagree about three paths takes three builds to learn three facts. And the
// walk is the only place with enough context to name both parties: it sees the
// whole set symmetrically, so it can say which two definitions claimed an
// address and where each was written. Eager composition could only ever
// attribute a collision to whichever app happened to compose SECOND, which is
// wiring-file line order and means nothing.
func conflicts(occ []occurrence) error {
	type claim struct {
		by   string
		site callsite
		via  string
	}
	held := make(map[string]claim, len(occ))
	var errs []error
	for _, o := range occ {
		r, ok := o.route()
		if !ok {
			continue
		}
		key := addrKey(r.method, o.abs(r.path))
		mine := claim{by: o.in.label(), site: o.site, via: o.ctx.trail.String()}
		if prev, dup := held[key]; dup {
			errs = append(errs, fmt.Errorf("zip: %s: declared by %q at %s (via %s) and by %q at %s (via %s)",
				key, prev.by, prev.site, prev.via, mine.by, mine.site, mine.via))
			continue
		}
		held[key] = mine
	}
	return errors.Join(errs...)
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
