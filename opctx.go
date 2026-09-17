package zip

import "context"

// opKey carries the operation being served. Its own type, so nothing else can
// collide with it or read it by guessing a string.
type opKey struct{}

// OpOf is the operation whose handler is running, and whether there is one.
//
// It answers the question a handler cannot answer for itself: WHERE it is being
// asked from. The path is the address the op is served at — a group's prefix
// composed with the leaf, resolved for the occurrence answering this call, which
// is not knowable at registration. A definition composed under two hosts has two
// addresses and one handler, so the address is a property of the CALL.
//
// That is why this is a context value rather than something a group could hand
// out at declaration: a prefix read at registration is the local join, and a
// consumer that built an address from it published one that nothing served the
// moment its definition was composed twice.
//
// There is no operation exactly when a handler is called directly rather than
// through the declaration that registered it — in a unit test, say — which is
// worth naming rather than papering over with a guessed address.
//
//	func (a *API) Livetail(ctx context.Context, in *Tail) (*Stream, error) {
//		op, ok := zip.OpOf(ctx)
//		if !ok {
//			return nil, fmt.Errorf("livetail: called outside its registration")
//		}
//		return a.relay(ctx, op.Path, in)
//	}
func OpOf(ctx context.Context) (Op, bool) {
	op, ok := ctx.Value(opKey{}).(Op)
	return op, ok
}

// withOp states the operation being served. The seam that KNOWS the occurrence
// calls it — the REST route, which matched, and the by-name seams, which looked
// the op up in the composed registry — so the address in the context is the
// resolved one.
func withOp(ctx context.Context, op Op) context.Context {
	return context.WithValue(ctx, opKey{}, op)
}

// declaredOp states the operation as DECLARED, and only if no seam has stated a
// resolved one. It is the floor under [OpOf]: every projection funnels through
// the one contract, so a handler always has an operation to read, and a seam
// that can do better than the declaration overrides this by arriving first.
func declaredOp(ctx context.Context, op Op) context.Context {
	if _, ok := ctx.Value(opKey{}).(Op); ok {
		return ctx
	}
	return withOp(ctx, op)
}

// servedOp is the identity of an op taken from the COMPOSED registry, where the
// path is already the address it answers at. The by-name seams — an MCP
// tools/call, the call plane, the in-process CLI — resolve an op by looking it
// up there, so what they hold is the occurrence, and this states it.
func servedOp(op *registeredOp) Op {
	return Op{Method: op.Method, Path: op.Path, OperationID: opName(op)}
}
