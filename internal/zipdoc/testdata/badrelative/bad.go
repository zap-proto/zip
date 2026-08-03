// Package badrelative compiles fine and cannot be filed: its typed op is
// registered at a RELATIVE path on a router that arrived as a parameter, so the
// address it will answer on is genuinely unknown here. Generation has to fail on
// it — a typed op filed under an identity this pass invented is a hole in the
// schema surface.
package badrelative

import (
	"context"

	"github.com/zap-proto/zip"
)

// In is the request.
type In struct {
	Name string `json:"name"`
}

// Out is the response.
type Out struct {
	OK bool `json:"ok"`
}

// Mount wires the one unfilable operation.
func Mount(app zip.Router) { zip.Post(app, "things", Create) }

// Create makes a thing.
func Create(_ context.Context, in *In) (*Out, error) { return &Out{OK: in.Name != ""}, nil }
