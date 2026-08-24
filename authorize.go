package zip

import (
	"context"
	"errors"
	"fmt"
)

// Op identifies a typed handler to an [Authorizer], so a decision can key on the
// operation as well as on the input.
//
// AS DECLARED, which for Path is the path written at the registration plus
// whatever prefix the registrar reported — not the address the composition
// finally answers at. A definition declared on a [App.Group] leaves its prefix to
// the walk (see [App.OpScope]), so an op written as Get(app.Group("/v1"), "/x")
// reports "/x" here and answers at "/v1/x". OperationID is the resolved id — the
// explicit [WithOperationID], else the method+path default.
//
// So a rule that must key on WHERE an op answers should take the prefix into its
// registrar (a Router that reports one) or declare the path whole, rather than
// read an absolute address off this. Method and OperationID carry no such
// qualification and are the same value everywhere.
type Op struct {
	Method      string
	Path        string
	OperationID string
}

// Authorizer authorizes a decoded, validated typed request at the op-invoke
// seam — the ONE point every projection of a typed handler funnels through. It
// runs after the request is decoded into the op's typed In and validated, and
// BEFORE the handler runs, over REST and MCP alike, so the value it authorizes
// is exactly the value the handler will act on: there is no second parse of the
// body for it to diverge from. in is the *In the handler will receive.
//
// Returning a non-nil error aborts the op before the handler runs, and that
// error is the response — return a zip.Err* (e.g. ErrForbidden) for a clean
// status.
type Authorizer func(ctx context.Context, op Op, in any) error

// Authorize installs fn as the op-invoke authorization rule. It is the op-level
// counterpart to Use: Use wraps the whole request with transport middleware,
// which for a body request sees only the raw bytes; Authorize runs one decision
// on the DECODED typed input of every op, REST and MCP alike — the seam a
// mounted subsystem asks so the value it authorizes is the value the handler
// binds. Call once while mounting, before Listen — the field is read on serve
// goroutines and writing it under load is a race. A nil fn declares no rule.
//
// IT COVERS EVERY OP THIS APP SERVES: the ones it registered itself, the ones
// declared on its groups, and the ones a definition it composed with [App.Use]
// brought with it. All three answer at this app's addresses, at its /mcp, at its
// call plane, in its graph and to its CLI, so all three are its ops. Composition
// is what decides that, so [adopt] settles it at build rather than each
// registration guessing.
//
// A definition may declare a TIGHTER rule of its own and it wins, because
// [App.rule] asks its own first. It cannot declare a looser one: declaring none
// falls through to whatever composed it, so composing a definition can only add
// a rule, never drop one.
//
// A definition composed under two rules that DISAGREE is refused at [App.Build]:
// its ops are one closure and one closure answers to one rule, so there is no
// honest way to serve it under both. See [adopt].
func (a *App) Authorize(fn Authorizer) { a.authorizer = fn }

// rule is the Authorizer in force over this App's ops: its own if it declared
// one, else the one it was composed under.
//
// Asked at invoke rather than settled at registration, so [App.Authorize],
// [App.Group] and [App.Use] are independent of each other and of order: a rule
// declared after the paths were grouped still covers them, a rule declared
// before still covers a group made later, and a definition written with no idea
// who would compose it still answers to whoever did.
func (a *App) rule() Authorizer {
	if a.authorizer != nil {
		return a.authorizer
	}
	if over := a.over.Load(); over != nil {
		return over.authorizer
	}
	return nil
}

// adopt settles which rule governs each definition in a composition, and is the
// only place that can: a definition is written without knowing what will include
// it, and what includes it is what decides whose ops its ops become.
//
// The rule over a definition is the nearest one declared ABOVE it on the path
// the walk took to reach it. A group has no rule of its own, so the walk carries
// straight through it to the app it groups; a composed definition that declares
// none likewise answers to its includer. One traversal answers both, because a
// group and a composed definition are one mechanism here.
//
// TWO INCLUDERS THAT DISAGREE ARE REFUSED, with both paths named. A definition's
// ops are one closure — composing it does not copy them — so serving it under
// two rules would mean one closure answering to two, which no implementation can
// do. The alternative is to keep whichever composition ran last, and half the
// time that is the looser of the two: a surface that reads as governed and is
// not. A build that stops says so; a surface that quietly serves does not.
//
// A definition composed BOTH under a rule and under no rule takes the rule. That
// is not a disagreement — nothing was said in the second place — and taking it
// is the answer that cannot be too permissive.
func adopt(occ []occurrence) error {
	var errs []error
	for _, o := range occ {
		t := o.ctx.trail
		if t == nil {
			continue
		}
		over := ruling(t)
		if over == nil || over == t.def {
			// Nothing above declared a rule, or this definition declared its own.
			continue
		}
		if prev := t.def.over.Load(); prev != nil && prev != over {
			errs = append(errs, fmt.Errorf(
				"zip: %s is composed under two rules that disagree — %s declares one and so does %s. "+
					"Its ops are one closure and answer to one rule, so name the rule on %s itself "+
					"or compose it under only one of them",
				t.label, over.label(), prev.label(), t.label))
			continue
		}
		t.def.over.Store(over)
	}
	return errors.Join(errs...)
}

// ruling is the nearest rule declared at or above t on the path the walk took.
// nil when nothing on that path declared one.
func ruling(t *trail) *App {
	for ; t != nil; t = t.up {
		if t.def.authorizer != nil {
			return t.def
		}
	}
	return nil
}

// Build constructs and installs a generation without starting a listener: the
// walk runs, every validation runs, the projections are rendered, and the
// router goes live — exactly what [App.Listen] does minus the sockets.
//
// It RETURNS THE VERDICT, which is the whole reason it replaced Prepare. A
// composition can be invalid — two definitions claiming one address, a cycle —
// and the old Prepare returned nothing, so the only way to learn was to start
// a server. Now a codegen step, a test or a wiring file's own main can ask
// whether the program it just wrote is a program, and get every conflict at
// once with both claimants named.
//
// Idempotent for the projections (they render once) and monotonic for the
// freeze. Listen calls it; there is no other way to build.
func (a *App) Build() error {
	// VALIDATE FIRST. prepare() renders the projections, and a projection of a
	// program that does not compose now panics — so validating after it would
	// turn every build error into a panic escaping the one function whose whole
	// job is to return it.
	if err := verify(a); err != nil {
		return err
	}
	a.prepare()
	a.buildMu.Lock()
	g, err := a.build()
	if err == nil {
		a.install(g)
	}
	a.buildMu.Unlock()
	return err
}
