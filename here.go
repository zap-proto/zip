package zip

import (
	"context"
	"fmt"
	"sync"
)

// here.go — the peer that is not a peer.
//
// [Ask] names an app and reaches it over that app's canonical socket. When the
// app is THIS process, that socket is a loop: the value the caller holds is
// serialized, handed to the kernel, read back, parsed into a fresh copy, and
// dispatched to a function pointer that was in memory the whole time. Nothing
// about the call needed a wire; only the ADDRESSING did, and addressing is
// exactly what a by-name call already has.
//
// So a bound listener records which App answers for it, [Serving] reads that
// back, and [Here] runs the op through the op's OWN invoke seam — the same
// validate → authorize → run core a REST request, an MCP tools/call, a CLI
// command and a socket call all land on. It is one fewer transport, not one more:
// the contract is the op, and only the encoding was ever the transport's.
//
// This is what a co-resident caller uses INSTEAD of building a request for its
// own router to take apart again. That pattern cost cloud a re-entrancy counter
// keyed on a goroutine id parsed out of runtime.Stack, because dispatching a
// request into the whole app re-ran every edge middleware, and a middleware that
// read the app it was serving recursed without bound. A direct op call cannot
// express that shape: it enters one op, not one router.

// serving maps a bound address to the App answering on it. The ADDRESS is the
// key rather than a name, because an address is what a listener has and
// [SocketPath] is already the one mapping from a name to it — so this holds no
// second opinion about where an app lives.
var serving sync.Map // addr -> *App

// bound records that a answers at addr, and returns the undo. Called by
// listenOn for every address it binds.
func bound(addr string, a *App) func() {
	serving.Store(addr, a)
	return func() { serving.Delete(addr) }
}

// Serving returns the App THIS PROCESS serves at name's canonical socket, or
// nil when the app is somewhere else.
//
// It answers a question a caller has to be able to ask before it spends a wake
// or a dial on a peer that is already in memory. It is a fact about the process,
// not a configuration: an app is here because something in this program bound
// its socket.
//
// Nil is the ordinary answer — a fleet of one-app processes is every app but
// one — and it means "not here", never "not deployed". Absence in the DEPLOYMENT
// is the router's word.
func Serving(name string) *App {
	if a, ok := serving.Load(SocketPath(name)); ok {
		return a.(*App)
	}
	return nil
}

// Here invokes op on a — in this process, with no wire under it.
//
// It is [Call] with the Conn removed. The op is found by the same [App.opByName]
// the socket plane uses and run through the same invoke seam, so it validates
// and authorizes identically; what it does NOT do is encode the input, because
// there is nothing between the two ends to encode it for.
//
// The reply is the handler's OWN value. Over a wire a caller gets a fresh copy
// because a wire cannot hand back anything else; here it does not, and a reply
// is a reply — read it, do not write it.
//
// In and Out must be the pair the op declared. A socket call learns that from a
// decode failure at the far end; here it is a type assertion, and the error says
// so plainly.
//
// A HELD OP IS NOT AN OUT. When the rule approves rather than allows, the
// handler does not run and there is no reply to hand back, so the [Approval]
// comes back on the error lane and [HeldOf] reads it — the same answer the 202
// carries over REST and the call plane.
func Here[In, Out any](ctx context.Context, a *App, op string, in *In) (*Out, error) {
	if a == nil {
		return nil, fmt.Errorf("zip: %s: no app here", op)
	}
	// opByName is the ONE by-name lookup — the same one the call plane and
	// tools/call run. An op this app does not answer is the same error whichever
	// way it was reached.
	o := a.opByName(op)
	if o == nil || o.direct == nil {
		return nil, ErrNotFound("unknown op: " + op)
	}
	out, err := o.direct(ctx, in)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	// Held, not mistyped. Surfacing the Approval before the assertion is what
	// keeps a policy outcome from being reported as the caller's type mistake.
	if a, ok := out.(*Approval); ok {
		return nil, a
	}
	reply, ok := out.(*Out)
	if !ok {
		return nil, fmt.Errorf("zip: %s answers %T, not %T", op, out, reply)
	}
	return reply, nil
}
