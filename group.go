package zip

import "fmt"

// A GROUP is a path prefix and everything declared under it; an OPERATION is
// one typed route. Three nouns carry the whole router — [App], [Group],
// [Operation] — and each is a concrete type, so the verb methods can be GENERIC
// METHODS, which Go 1.27 allows on concrete types and still refuses on
// interfaces. In and Out are inferred from the handler and the caller writes no
// type arguments at all:
//
//	users := app.Group("/v1/users")
//	users.Post("/", api.Create)
//
//	mint := users.Group("/mint").Use(middleware.Mint(gate))
//	mint.Post("/deposit", api.Deposit)
//
// THE HANDLER IS THE BOUND METHOD, passed directly rather than wrapped in a
// closure or built by a factory, so `api.Create` survives in the source where
// the projections resolve it back to the method and its doc comment. A wrapper
// erases that, and with it everything the operation publishes about itself.
//
// A Group is a CLOSED HANDLE. There is no App(), no Parent(), no Root() — a
// subsystem handed the group at its own prefix cannot reach above it, so
// confinement is a property of the type rather than a rule something has to
// police. That is what lets a host hand out a bounded surface and know what
// came back.

// Group is a prefix, the middleware that holds under it, and the options every
// operation declared there carries. [App.Group] opens one; [Group.Group] nests.
type Group struct {
	// on is the definition this group registers into — an *App with a prefix,
	// which is what a group has always been. It is unexported and has no
	// accessor: see the note above.
	on   *App
	opts []OpOption
}

// Group opens a group at prefix. Middleware passed here applies to every route
// under it.
func (a *App) Group(prefix string, mw ...Handler) *Group {
	return &Group{on: a.group(here(1), prefix, mw...)}
}

// Group nests a group inside this one: the prefixes compose, and the outer
// group's options are carried into the inner one rather than shared with it, so
// a tag added below does not reach back up.
func (g *Group) Group(prefix string, mw ...Handler) *Group {
	return &Group{
		on:   g.on.group(here(1), prefix, mw...),
		opts: append([]OpOption(nil), g.opts...),
	}
}

// Use composes middleware, or another [App] included by reference, into this
// group. It is the ONE composition verb.
func (g *Group) Use(cs ...Component) *Group {
	// A scoped chain composes around leaves registered THROUGH this group, and a
	// definition's leaves belong to the definition — wrapping them would mean
	// editing an App other hosts may also compose, and composing it UNGATED is
	// the one outcome that must not happen silently, since the chain people
	// reach for With to install is usually the gate.
	if g.on.wrap != nil {
		for _, c := range cs {
			if def, isApp := c.(*App); isApp {
				panic(fmt.Sprintf("zip: With(...).Use(%s): a scoped chain cannot wrap a "+
					"definition's own leaves — group the definition and scope the chain "+
					"there: app.Group(prefix).Use(def)", def.who()))
			}
		}
	}
	g.on.Use(cs...)
	return g
}

// Raw registers a route that is NOT a typed operation: a webhook that answers
// bytes, a stream, an upgrade, a health probe. There is no In and no Out to
// infer, so it is not projected into the document, the tool list or the SDKs —
// see [registeredOp].
//
// It is deliberately the longer spelling. Opting out of the typed model is the
// exception, and it should read like one.
func (g *Group) Raw(method, path string, handlers ...Handler) *Group {
	g.on.raw(here(1), method, path, handlers)
	return g
}

// Tag adds tags to every operation declared under this group.
func (g *Group) Tag(tags ...string) *Group {
	g.opts = append(g.opts, func(op *registeredOp) { op.Tags = append(op.Tags, tags...) })
	return g
}

// Defaults carries any [OpOption] as a default for this group's operations. An
// option the operation itself sets is applied after this one and wins.
func (g *Group) Defaults(opts ...OpOption) *Group {
	g.opts = append(g.opts, opts...)
	return g
}

// With returns a group whose every leaf handler is wrapped by mw — the chain
// composes around the TERMINAL, never around the middleware preceding it, so
// specificity precedence is untouched and a leaf registered on the returned
// group later is wrapped too.
//
// It differs from [Group.Use], which puts middleware in the chain as an entry
// of its own. Reach for With when the chain must hold at the leaf, for Use when
// it holds over the group.
func (g *Group) With(mw ...Middleware) *Group {
	return (&Group{
		on:   g.on.group(here(1), ""),
		opts: append([]OpOption(nil), g.opts...),
	}).chain(mw)
}

// chain installs the middleware on a group already created for it. A group carries
// its parent's chain (see [App.group]), so a chain added here composes with
// that one rather than replacing it — a gate installed above stays installed.
func (g *Group) chain(mw []Middleware) *Group {
	if chain := Chain(mw...); g.on.wrap == nil {
		g.on.wrap = chain
	} else {
		g.on.wrap = Chain(g.on.wrap, chain)
	}
	return g
}

// OAuth marks every route declared on the group it returns as answering
// RFC 6749 §5.2 rather than RFC 9457, so the vocabulary is a property of where
// a route was declared and not of what its handler returns.
func (g *Group) OAuth() *Group {
	n := g.Group("")
	n.on.oauth = true
	return n
}

// Undeclared marks every route declared on the group it returns as serving but
// left out of [App.Declaration].
func (g *Group) Undeclared() *Group {
	n := g.Group("")
	n.on.undeclared = true
	return n
}

// Authorize installs fn as the rule over the ops declared on THIS group, and
// only those. It is [App.Authorize] at a prefix, with the same semantics: asked
// at invoke on the decoded input, a tighter rule than the one composed above it
// wins, and declaring none here falls through to that one rather than dropping
// it.
//
// The distinction is the whole point of having it. A subsystem that guards its
// own writes must put the rule on the GROUP it guards: on the group, every op
// under it answers the rule; on the host app, the rule becomes the HOST's, and
// [App.Authorize] covers every op the host serves — including a sibling
// subsystem's, whose ops would then be judged by rules written about a principal
// this guard never attached. Refusing every valid call to an unrelated
// subsystem is the failure that shape produces, and putting the rule nowhere is
// the other one: an authenticated write that nothing checked.
func (g *Group) Authorize(fn Authorizer) *Group {
	g.on.authorizer = fn
	return g
}

// Get declares a GET operation.
func (g *Group) Get[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return g.declare("GET", path, fn, opts)
}

// Post declares a POST operation.
func (g *Group) Post[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return g.declare("POST", path, fn, opts)
}

// Put declares a PUT operation.
func (g *Group) Put[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return g.declare("PUT", path, fn, opts)
}

// Patch declares a PATCH operation.
func (g *Group) Patch[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return g.declare("PATCH", path, fn, opts)
}

// Delete declares a DELETE operation. A DELETE addresses what it deletes with
// its URL and carries no request body — see [hasBody].
func (g *Group) Delete[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return g.declare("DELETE", path, fn, opts)
}

// declare is the one registration every verb method shares. The depth is one:
// the route belongs to the caller of the verb method, and this frame is the
// single one of this package between them. TestSite_IsTheWrittenLine holds it.
func (g *Group) declare[In, Out any](method, path string, fn TypedHandler[In, Out], opts []OpOption) *Operation[In, Out] {
	all := append(append([]OpOption(nil), g.opts...), opts...)
	return &Operation[In, Out]{op: registerTyped(1, g.on, method, path, fn, all...)}
}

// Operation is a declared typed route. Its methods carry the metadata that
// cannot be inferred — everything else (method, path, input, output, the
// handler's documentation) is already known from the declaration.
//
// It is the same record the authorizer sees as [Op] and the document publishes;
// this is the handle the declaring code holds.
type Operation[In, Out any] struct{ op *registeredOp }

// ID sets the operation id. Without one the method and path name the operation.
func (o *Operation[In, Out]) ID(id string) *Operation[In, Out] {
	o.op.OperationID = id
	return o
}

// Summary sets the one-line summary the document publishes.
func (o *Operation[In, Out]) Summary(s string) *Operation[In, Out] {
	o.op.Summary = s
	return o
}

// Tag adds tags to this operation, after any its group declared.
func (o *Operation[In, Out]) Tag(tags ...string) *Operation[In, Out] {
	o.op.Tags = append(o.op.Tags, tags...)
	return o
}

// Status declares the success codes this operation answers with — see
// [WithStatus] for what more than one means.
func (o *Operation[In, Out]) Status(codes ...int) *Operation[In, Out] {
	return o.With(WithStatus(codes...))
}

// With applies any [OpOption], so the chain never becomes the only way to say
// something the option vocabulary already says.
func (o *Operation[In, Out]) With(opts ...OpOption) *Operation[In, Out] {
	for _, opt := range opts {
		opt(o.op)
	}
	return o
}

// Alias registers ONE handler at TWO addresses — the canonical one and a legacy
// spelling kept reachable for consumers pinned to it.
//
// A path segment names a THING; the HTTP method says what is being done to it.
// The verb-noun addresses a service inherits (`send-verification-code`,
// `set-preferred-mfa`, …) say the verb twice, and they are what a customer reads
// in a CLI's help, in every generated SDK method name and on every docs page. The
// canonical noun is what the published document leads with; the legacy spelling
// stays reachable so nothing breaks while consumers move.
//
// One handler VALUE, two addresses: there is no second implementation to keep in
// step, and no forward that could answer differently from the thing it forwards
// to. When the last pinned consumer moves, the legacy half is deleted and nothing
// else changes.
//
// It lives here rather than in each service because cmd/zipdoc has to recognise
// it. A registration made inside a helper is invisible to a pass that reads
// group.Raw(method, path, handler) calls, so a service that rolled its own alias
// helper silently lost the prose for BOTH addresses — the exact defect this
// package exists to prevent. Being zip's, both halves carry the handler's doc
// comment.
func (g *Group) Alias(method, canonical, legacy string, h Handler) *Group {
	g.Raw(method, canonical, h)
	g.Raw(method, legacy, h)
	return g
}
