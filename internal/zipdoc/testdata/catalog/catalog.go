// Package catalog is similarity search over your own embeddings.
//
// The paragraph past the synopsis is ordinary prose and stays that way. It even
// contains a line that opens with a capitalised word and a colon, because package
// docs are full of them:
//
// Note: only the declared keys are markers.
//
// Product:    Hanzo Vector
// Category:   data
// Kind:       api
// Visibility: public
// Meters:     per-GB-month
// Backup:     sqlite:/data/vector.db retention=30d
package catalog

import (
	"context"

	"github.com/zap-proto/zip"
)

//go:generate zipdoc

// In asks a question of the index.
type In struct {
	// Query is the text to embed and search for.
	Query string `json:"query"`
}

// Out is the answer.
type Out struct {
	// Hits are the nearest neighbours, closest first.
	Hits []string `json:"hits"`
}

// Register wires the one operation.
func Register(app *zip.App) { zip.Post(app, "/v1/vector/search", Search) }

// Search returns the nearest neighbours of the query text.
func Search(_ context.Context, in *In) (*Out, error) { return &Out{Hits: []string{in.Query}}, nil }
