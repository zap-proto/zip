// Package defined answers with a type defined over another struct, whose
// fields are the other's and whose prose is written there.
package defined

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/zap-proto/zip/internal/zipdoc/testdata/defined/court"
)

// Filing is one filing.
type Filing struct {
	// Name is the filing's title.
	Name string `json:"name"`
}

// legalFiling is a Filing as this package answers it.
type legalFiling Filing

// docket is a court record as this package answers it.
type docket court.Record

// In names a filing.
type In struct {
	// ID addresses the filing.
	ID string `json:"id" url:"id"`
}

// Get returns one filing.
func Get(ctx context.Context, in *In) (*legalFiling, error) { return &legalFiling{}, nil }

// Docket returns the filing's court record.
func Docket(ctx context.Context, in *In) (*docket, error) { return &docket{}, nil }

// Register declares the ops.
func Register(app *zip.App) {
	app.Get("/v1/filings/:id", Get)
	app.Get("/v1/filings/:id/docket", Docket)
}
