// Package mounted is a subsystem the way a host mounts one: its Mount takes a
// zip.Router, so the prefix it will sit under is known only to the caller, in
// another package. Its typed ops are registered at ABSOLUTE paths, which is what
// makes them filable anyway.
package mounted

import (
	"context"

	"github.com/zap-proto/zip"
)

//go:generate zipdoc

// In asks for one thing.
type In struct {
	// ID of the thing.
	ID string `json:"id"`
}

// Out is the thing.
type Out struct {
	// Name of the thing.
	Name string `json:"name"`
}

// Mount wires the subsystem onto whatever router the host hands it.
func Mount(app zip.Router) {
	zip.Get(app, "/v1/mounted/things/:id", Read)
	// A raw route on the same router, for the same reason and by the same rule.
	app.Get("/v1/mounted/health", func(c *zip.Ctx) error { return nil })
}

// Read returns one thing by id.
func Read(_ context.Context, in *In) (*Out, error) { return &Out{Name: in.ID}, nil }
