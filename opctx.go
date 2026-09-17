package zip

import "context"

// opKey carries the invocation's operation. Its own type, so nothing else can collide
// with it or read it by guessing a string.
type opKey struct{}

// invocation is what a handler may ask about the invocation it is inside: which
// operation, and the address this particular call was made to.
//
// The address is a FUNCTION because computing it reads the decoded input, and
// most ops never ask. An op whose pattern carries no parameter pays a string
// compare; one that does pays reflection, and only if something reads it.
type invocation struct {
	op   Op
	addr func() string
}

// OpOf is the operation whose handler is running, and whether there is one.
//
// The path is the PATTERN the operation is served at — a group's prefix composed
// with the leaf, resolved for the occurrence answering this call, which is not
// knowable at registration. A definition composed under two hosts has two
// addresses and one handler, so it is a property of the CALL.
//
// That is why this is a context value rather than something a group hands out at
// declaration: a prefix read at registration is the local join, and an address
// built from it named something nothing served the moment the definition was
// composed twice.
//
// There is no operation exactly when a handler is called directly rather than
// through the declaration that registered it — in a unit test, say — which is
// worth naming rather than papering over with a guessed address.
func OpOf(ctx context.Context) (Op, bool) {
	c, ok := ctx.Value(opKey{}).(*invocation)
	if !ok {
		return Op{}, false
	}
	return c.op, true
}

// AddressOf is the address THIS call was made to, with the pattern's parameters
// filled in from the input — "/v1/traces/:id" answered as "/v1/traces/abc".
//
// It is what a handler that FORWARDS needs: the pattern says which operation,
// the address says which resource, and a relay has to name both. It is computed
// from the same values [Address] takes, at the one seam that holds them both, so
// every projection gets the same answer — including a tools/call or an
// in-process invoke, where there is no request to read a path from.
//
// It is deliberately NOT a field on [Op]. Op is what the [Authorizer] is handed
// and what [App.OnResult] is told, and both are narrow on purpose: the result
// hook is given the operation and the outcome and never the input. A concrete
// address carries path parameters, which are input, so putting it there would
// widen two contracts as a side effect of serving a third. A handler asks for
// it; a recorder is not handed it.
func AddressOf(ctx context.Context) (string, bool) {
	c, ok := ctx.Value(opKey{}).(*invocation)
	if !ok || c.addr == nil {
		return "", false
	}
	return c.addr(), true
}

// withOp states the operation being served, before the input is decoded. The
// seam that KNOWS the occurrence calls it — the REST route, which matched, and
// the by-name seams, which looked the op up in the composed registry — so the
// pattern in the context is the resolved one.
func withOp(ctx context.Context, op Op) context.Context {
	return context.WithValue(ctx, opKey{}, &invocation{op: op})
}

// withAddress restates the call once the input is known, so [AddressOf] can
// answer. It keeps whatever pattern a seam already resolved and falls back to
// the declaration when none did.
func withAddress[In any](ctx context.Context, declared Op, in *In) context.Context {
	op := declared
	if c, ok := ctx.Value(opKey{}).(*invocation); ok {
		op = c.op
	}
	pattern := op.Path
	return context.WithValue(ctx, opKey{}, &invocation{
		op:   op,
		addr: func() string { return Address(pattern, in) },
	})
}

// servedOp is the identity of an op taken from the COMPOSED registry, where the
// path is already the address it answers at. The by-name seams — an MCP
// tools/call, the call plane, the in-process CLI — resolve an op by looking it
// up there, so what they hold is the occurrence, and this states it.
func servedOp(op *registeredOp) Op {
	return Op{Method: op.Method, Path: op.Path, OperationID: opName(op)}
}
