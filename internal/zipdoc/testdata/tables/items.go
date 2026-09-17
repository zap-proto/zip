package tables

import (
	"context"

	"github.com/zap-proto/zip"
)

// ItemIn describes an item input.
type ItemIn struct {
	// Name of the item.
	Name string `json:"name"`
	// Price in cents.
	Price int64 `json:"price"`
}

// ItemOut describes an item output.
type ItemOut struct {
	// ID of the created item.
	ID string `json:"id"`
	// OK indicates status.
	OK bool `json:"ok"`
}

type API struct{}

// CreateItem creates a new catalog item.
//
// Example: {"name": "Widget", "price": 999}
// Response: {"id": "itm_1", "ok": true}
func (a *API) CreateItem(_ context.Context, in *ItemIn) (*ItemOut, error) {
	return &ItemOut{ID: "itm_1", OK: true}, nil
}

// LegacyCreate creates a new catalog item via legacy registrar.
//
// Example: {"name": "Widget", "price": 999}
// Response: {"id": "itm_1", "ok": true}
func LegacyCreate(_ context.Context, in *ItemIn) (*ItemOut, error) {
	return &ItemOut{ID: "itm_1", OK: true}, nil
}

func Register(app *zip.App) {
	// At the root, where the app itself is the table.
	app.Post("/v1/legacy/items", LegacyCreate)

	// On a group, with a metadata chain after the declaration.
	v1 := app.Group("/v1").Tag("catalog")
	items := v1.Group("/items")
	items.Post("/", (&API{}).CreateItem).
		ID("items.create").
		Summary("Create an item")
}
