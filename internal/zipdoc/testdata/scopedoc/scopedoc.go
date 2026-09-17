package scopedoc

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
	// Legacy package-level registration
	zip.Post(app.Group("/v1"), "/legacy/items", LegacyCreate)

	// New concrete-scope method registration with fluent chaining
	v1 := app.Scope("/v1").Tag("catalog")
	items := v1.Scope("/items")
	items.Post("/", (&API{}).CreateItem).
		ID("items.create").
		Summary("Create an item")
}
