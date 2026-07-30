// Package crosspkg registers ops whose In and Out are declared in another
// package, and whose handler lives in a third file of this one.
package crosspkg

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/zap-proto/zip/internal/zipdoc/testdata/plane"
)

//go:generate zipdoc

// Register wires the service.
func Register(app *zip.App) {
	zip.Post(app, "/v1/workers", ListWorkers)
}

// ListWorkers returns every worker in the org, newest first.
//
// Example: {"org": "hanzo", "limit": 25}
func ListWorkers(_ context.Context, in *plane.ListIn) (*plane.ListOut, error) {
	return &plane.ListOut{Next: in.Org}, nil
}
