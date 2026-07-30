package zip

import (
	"context"
	"sync"
)

// conns is the process's one Conn per peer name. A [Conn] is safe for
// concurrent use and holds a POOLED transport, so the right lifetime for one is
// the process, not the call: dialing per call throws the pool away and turns
// every plane hop into a fresh connect, which on a hot path is the whole cost.
//
// Keyed by name rather than by resolved path so that a redeployed peer — same
// name, same socket, new process — is reached by the same handle: the socket
// path is the identity (see [SocketPath]), and reconnecting is the transport's
// job, not the caller's.
var conns sync.Map // name -> *Conn

// Ask invokes one op on a named peer and returns its reply. It is the whole of
// what a caller needs to reach another service:
//
//	out, err := zip.Ask[plane.AuthorizeIn, plane.AuthorizeOut](
//		ctx, "billing", plane.FinanceAuthorize, &in)
//
// The peer is reached over its canonical socket ([SocketPath]) — no registry, no
// discovery, no address in the caller's configuration. The op token is the
// operation's one identity: the same string is its operationId, its MCP tool
// name and its CLI command.
//
// Identity is not invented here. Whatever the edge asserted about the caller
// rides along when ctx carries a request ([Ctx.Forward]); a background job
// states one explicitly with [WithCaller]. There is no org argument, because an
// org in the argument is an org the caller chose.
//
// The Conn is kept for the life of the process. Nothing needs closing, and a
// caller that closes one only drops its idle connections — the next Ask
// redials.
func Ask[In, Out any](ctx context.Context, name, op string, in *In) (*Out, error) {
	c, err := peer(name)
	if err != nil {
		return nil, err
	}
	return Call[In, Out](ctx, c, op, in)
}

// peer returns the process's Conn to name, dialing on first use. Two callers
// racing the first Ask both dial; one handle wins and the loser's is dropped
// unused, which costs nothing because dialing is lazy.
func peer(name string) (*Conn, error) {
	if c, ok := conns.Load(name); ok {
		return c.(*Conn), nil
	}
	c, err := DialApp(name)
	if err != nil {
		return nil, err
	}
	if got, loaded := conns.LoadOrStore(name, c); loaded {
		return got.(*Conn), nil
	}
	return c, nil
}
