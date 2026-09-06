// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

// A store, written to exercise the half of an op that a read-only service never
// reaches: a body, a path parameter, a header, a declared status and a declared
// id. It is the second half of the corpus — cpp/example/store is the same
// service in C++, and the two must project to the same bytes.
package store

import (
	"context"

	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Item is one thing in the store.
type Item struct {
	// ID is what the store calls it.
	ID string `json:"id"`
	// Name is what a person calls it.
	Name string `json:"name"`
	// Count is how many there are.
	Count uint32 `json:"count"`
	// Tags are what it is filed under.
	Tags []string `json:"tags"`
}

// NewItem is what a caller must say to put one in the store.
type NewItem struct {
	// Name is what to call it.
	Name string `json:"name" validate:"required"`
	// Count is how many arrived.
	Count uint32 `json:"count"`
	// Tenant is who is asking, which the store reads from the request itself.
	Tenant string `json:"-" header:"X-Tenant" validate:"required"`
}

// ItemPatch is what a caller may change about one.
type ItemPatch struct {
	// ID is the item to change, which the address already names.
	ID string `json:"-" url:"id"`
	// Name is what to call it now.
	Name string `json:"name"`
	// Count is how many there are now.
	Count uint32 `json:"count"`
}

// Ask names one item.
type Ask struct {
	// ID is the item to answer for.
	ID string `json:"id" url:"id" validate:"required"`
}

// Gone is what is left of an item that has been removed.
type Gone struct {
	// ID is what it was called.
	ID string `json:"id"`
}

// Store keeps items.
type Store struct{}

// Ops is this service's typed operations.
func (s *Store) Ops() *zip.App {
	app := zip.New(zip.Config{
		AppName:               "store",
		DisableStartupMessage: true,
		OpenAPI: zip.OpenAPIConfig{
			Title:       "A store",
			Description: "Four ops that between them use every half of a request: a body, an address, a header, and a status that is not 200.",
			Version:     "v1.0.0",
		},
	})
	zip.Post(app, "/v1/items", s.add, zip.WithStatus(201), zip.WithTags("items"))
	zip.Get(app, "/v1/items/:id", s.one, zip.WithTags("items"))
	zip.Patch(app, "/v1/items/:id", s.change, zip.WithTags("items"))
	zip.Delete(app, "/v1/items/:id", s.drop, zip.WithOperationID("forget"), zip.WithTags("items"))
	return app
}

// Add puts an item in the store and answers with what it became.
//
// Example: {"name": "anvil", "count": 3}
// Response: {"id": "item-1", "name": "anvil", "count": 3, "tags": []}
func (s *Store) add(_ context.Context, in *NewItem) (*Item, error) {
	if in.Name == "" {
		return nil, zip.Errorf(400, "argument 'name' not given")
	}
	return &Item{ID: "item-1", Name: in.Name, Count: in.Count, Tags: []string{}}, nil
}

// One is the item that id names.
//
// Example: {"id": "item-1"}
// Response: {"id": "item-1", "name": "anvil", "count": 3, "tags": []}
func (s *Store) one(_ context.Context, in *Ask) (*Item, error) {
	if in.ID == "" {
		return nil, zip.Errorf(400, "argument 'id' not given")
	}
	return &Item{ID: in.ID, Name: "anvil", Count: 3, Tags: []string{}}, nil
}

// Change edits an item and answers with what it is now.
//
// Example: {"name": "anvil", "count": 4}
// Response: {"id": "item-1", "name": "anvil", "count": 4, "tags": []}
func (s *Store) change(_ context.Context, in *ItemPatch) (*Item, error) {
	return &Item{ID: in.ID, Name: in.Name, Count: in.Count, Tags: []string{}}, nil
}

// Drop removes an item, and says which one it was.
//
// Example: {"id": "item-1"}
// Response: {"id": "item-1"}
func (s *Store) drop(_ context.Context, in *Ask) (*Gone, error) {
	return &Gone{ID: in.ID}, nil
}
