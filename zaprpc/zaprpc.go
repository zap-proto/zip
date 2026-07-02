// Package zaprpc is an OPTIONAL named-service RPC dispatch helper for zip
// apps that want a Cap'n-Proto/gRPC-style service registry (name → method →
// handler) rather than plain REST routes.
//
// It is DECOUPLED from the transport: zip's primary transport is ZAP, wired
// once in the framework (App.Listen serves the whole fiber handler over
// zap-proto/http — the routes ARE the ZAP surface, no registry needed). This
// package is for the separate case where you want to expose generated
// zapc <svc>_server.go services by name; mount it as an ordinary route with
// zaprpc.HTTPHandler(registry) (see http.go), reachable over either transport.
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
