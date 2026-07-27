// Package badexample compiles fine and documents itself wrongly: its Example is
// not JSON. Generation has to fail on it, because the alternative is a spec that
// ships an example nobody can send.
package badexample

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

// Register wires the one broken operation.
func Register(app *zip.App) { zip.Post(app, "/v1/things", Create) }

// Create makes a thing.
//
// Example: {"name": "ada",}
func Create(_ context.Context, in *In) (*Out, error) { return &Out{OK: in.Name != ""}, nil }
