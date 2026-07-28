// Package zaprpc is an OPTIONAL named-service RPC dispatch helper: a
// Cap'n-Proto/gRPC-style service registry (name → method → handler) with its own
// envelope, mounted as an ordinary route via [HTTPHandler].
//
// # Deprecated: it is neither zip's call plane nor ZAP's wire format
//
// Nothing in zip uses it, and nothing needs to:
//
//   - To reach another service, use zip's op-call plane — zip.DialApp + zip.Call
//     address an op by its operation id, typed both ways, over whatever
//     transport the callee is listening on. That plane is a projection of the
//     typed-op registry, so it cannot drift from the routes, the document or the
//     MCP tools.
//   - For the ZAP wire format itself, use github.com/zap-proto/go/rpc, which
//     owns it. This package's envelope is a PARALLEL one that predates that and
//     does not interoperate with it: 24-byte header, service and method as
//     strings, no promise pipelining, against upstream's u32 ordinals. A peer
//     speaking one cannot decode the other.
//
// It stays here only so removing an exported package is a decision a release
// makes deliberately rather than a patch makes silently. Do not build on it.
package zaprpc

import (
	"context"
	"errors"
)

// Service is the minimal ZAP service interface zip can dispatch to.
// zapc-generated <svc>_server.go satisfies this naturally.
type Service interface {
	// Name returns the service identifier (e.g. "validate.v1").
	Name() string
	// Handle dispatches one RPC call on the service. method is the
	// fully-qualified method name; payload is the wire-encoded ZAP
	// request body; the returned bytes are the wire-encoded response.
	Handle(ctx context.Context, method string, payload []byte) ([]byte, error)
}

// Registry holds a set of named services, dispatched via HTTPHandler.
type Registry struct {
	services map[string]Service
}

// NewRegistry constructs an empty registry.
func NewRegistry() *Registry {
	return &Registry{services: map[string]Service{}}
}

// Register adds a service. Calling twice with the same name overwrites
// (caller bug).
func (r *Registry) Register(s Service) {
	r.services[s.Name()] = s
}

// Get returns the service for name, or nil.
func (r *Registry) Get(name string) Service {
	return r.services[name]
}

// Names returns the registered service names.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.services))
	for n := range r.services {
		out = append(out, n)
	}
	return out
}

// ErrNoService is returned by Dispatch when the service is unregistered.
var ErrNoService = errors.New("zaprpc: service not registered")

// Dispatch invokes the named service+method against the registry. The
// wire-decode happens upstream (HTTPHandler decodes the request envelope,
// then calls Dispatch to route it to the right handler).
func (r *Registry) Dispatch(ctx context.Context, service, method string, payload []byte) ([]byte, error) {
	s, ok := r.services[service]
	if !ok {
		return nil, ErrNoService
	}
	return s.Handle(ctx, method, payload)
}
