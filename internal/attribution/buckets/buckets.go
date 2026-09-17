// Package buckets writes registrations in every form zip offers, so a test can
// ask which package each one is filed under.
package buckets

import (
	"context"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/internal/attribution/meter"
)

// Path is the import path of this package, which is the answer every op
// registered here must give.
const Path = "github.com/zap-proto/zip/internal/attribution/buckets"

// In asks for a bucket.
type In struct {
	Name string `json:"name"`
}

// Out is one bucket.
type Out struct {
	Name string `json:"name"`
}

// API serves buckets.
type API struct{}

// Make makes a bucket.
func (API) Make(ctx context.Context, in *In) (*Out, error) { return &Out{Name: in.Name}, nil }

// List lists them.
func (API) List(ctx context.Context, in *In) (*Out, error) { return &Out{}, nil }

// Read reads one.
func (API) Read(ctx context.Context, in *In) (*Out, error) { return &Out{}, nil }

// Register declares the surface three ways: at the root, on a group, and on a
// group over a handler another package composed.
func Register(app *zip.App) {
	var api API
	app.Post("/plain", api.Make)

	g := app.Group("/scope")
	g.Get("/direct", api.List)
	g.Get("/paid", meter.Paid(api.Read))
}

// Clash declares one address twice through a group, so a test can read which
// line the conflict names.
func Clash(app *zip.App) {
	var api API
	g := app.Group("/scope")
	g.Get("/twice", api.List)
	g.Get("/twice", api.Read)
}
