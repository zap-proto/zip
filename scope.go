package zip

// A SCOPE is a path prefix and what holds under it; an OPERATION is one typed
// route declared there. The verb methods below are GENERIC METHODS on a
// concrete type, which Go 1.27 allows, so In and Out are inferred from the
// handler and the caller writes no type arguments at all:
//
//	accounts := app.Scope("/v1/accounts").Tag("accounts")
//
//	accounts.Get("/", api.ListAccounts)
//	accounts.Post("/", api.CreateAccount).ID("accounts.create")
//	accounts.Get("/:id", api.GetAccount)
//
// Interface methods still may not declare type parameters — that part of the
// restriction did not move — which is why these live on *Scope and not on
// [Router]. [Post] and its siblings remain the low-level form; both register
// the same op through the same path, so a service may hold either and the
// document, the MCP tool, the CLI and the call plane read one registry.
//
// THE HANDLER IS THE BOUND METHOD. It is passed directly rather than wrapped in
// a closure or built by a factory, so `api.CreateAccount` survives in the source
// where the projections can resolve it back to the method and its doc comment. A
// wrapper erases that, and with it the documentation the operation publishes.

// Scope is a prefix and the options every operation declared under it carries.
// It is created by [App.Scope] and nests with [Scope.Scope].
type Scope struct {
	on   Router
	opts []OpOption
}

// Scope opens a scope at prefix. Middleware passed here applies to every route
// under it, the same as [App.Group], which is what it opens the scope on.
func (a *App) Scope(prefix string, mw ...Handler) *Scope {
	return &Scope{on: a.Group(prefix, mw...)}
}

// Scope nests a scope inside this one: the prefixes compose, and the outer
// scope's options are carried into the inner one rather than shared with it, so
// a tag added below does not reach back up.
func (s *Scope) Scope(prefix string, mw ...Handler) *Scope {
	return &Scope{on: s.on.Group(prefix, mw...), opts: append([]OpOption(nil), s.opts...)}
}

// Tag adds tags to every operation declared under this scope.
func (s *Scope) Tag(tags ...string) *Scope {
	s.opts = append(s.opts, func(op *registeredOp) { op.Tags = append(op.Tags, tags...) })
	return s
}

// With carries any [OpOption] as a default for this scope's operations. An
// option the operation itself sets is applied after this one and wins.
func (s *Scope) With(opts ...OpOption) *Scope {
	s.opts = append(s.opts, opts...)
	return s
}

// Router exposes the scope's untyped surface, for the routes that are not a
// typed request and response: a webhook that answers bytes, a stream, an
// upgrade. Those have no In and Out to infer and are not projected — see
// [registeredOp].
func (s *Scope) Router() Router { return s.on }

// Get declares a GET operation under this scope.
func (s *Scope) Get[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return s.declare("GET", path, fn, opts)
}

// Post declares a POST operation under this scope.
func (s *Scope) Post[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return s.declare("POST", path, fn, opts)
}

// Put declares a PUT operation under this scope.
func (s *Scope) Put[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return s.declare("PUT", path, fn, opts)
}

// Patch declares a PATCH operation under this scope.
func (s *Scope) Patch[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return s.declare("PATCH", path, fn, opts)
}

// Delete declares a DELETE operation under this scope. A DELETE addresses what
// it deletes with its URL and carries no request body — see [hasBody].
func (s *Scope) Delete[In, Out any](path string, fn TypedHandler[In, Out], opts ...OpOption) *Operation[In, Out] {
	return s.declare("DELETE", path, fn, opts)
}

// declare is the one registration the verb methods share. The depth is 2
// because the route is attributed to the caller of the verb method, and there
// are two frames of this package between them.
func (s *Scope) declare[In, Out any](method, path string, fn TypedHandler[In, Out], opts []OpOption) *Operation[In, Out] {
	all := append(append([]OpOption(nil), s.opts...), opts...)
	return &Operation[In, Out]{op: registerTyped(2, s.on, method, path, fn, all...)}
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

// Tag adds tags to this operation, after any its scope declared.
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
