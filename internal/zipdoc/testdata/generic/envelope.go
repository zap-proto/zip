// Package generic answers through an instantiated generic, whose fields are
// documented once on the generic and filed under each instantiation's name.
package generic

import (
	"context"

	"github.com/zap-proto/zip"
)

// Envelope is the answer every op here wraps its data in.
type Envelope[T any] struct {
	// Status is ok or error.
	Status string `json:"status"`
	// Data is what the op answers.
	Data T `json:"data"`
}

// Store is one store.
type Store struct {
	// Name is the store's name.
	Name string `json:"name"`
}

// ListIn asks for stores.
type ListIn struct {
	// Owner is the org whose stores are listed.
	Owner string `json:"owner" url:"owner"`
}

// List returns the owner's stores.
func List(ctx context.Context, in *ListIn) (*Envelope[[]Store], error) {
	return &Envelope[[]Store]{Status: "ok"}, nil
}

// One returns one store.
func One(ctx context.Context, in *ListIn) (*Envelope[Store], error) {
	return &Envelope[Store]{Status: "ok"}, nil
}

// Register declares the ops.
func Register(app *zip.App) {
	app.Get("/v1/stores/:owner", List)
	app.Get("/v1/store/:owner", One)
}
