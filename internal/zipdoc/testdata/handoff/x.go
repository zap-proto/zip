// Package handoff hands a group to another function, which is the one shape a
// one-file pass cannot resolve: the prefix belongs to the call that made the
// group, and the call is somewhere else.
package handoff

import (
	"context"

	"github.com/zap-proto/zip"
)

// In is the input.
type In struct {
	A string `json:"a"`
}

// Out is the output.
type Out struct {
	B string `json:"b"`
}

// H answers.
func H(ctx context.Context, in *In) (*Out, error) { return &Out{B: in.A}, nil }

// Register hands the group across a call.
func Register(app *zip.App) {
	deeper(app.Group("/v1/thing"))
}

func deeper(g *zip.Group) {
	g.Post("/replay", H)
}
