// Package embedded answers with a struct that embeds another, so most of its
// fields are promoted and documented where they are declared.
package embedded

import (
	"context"

	"github.com/zap-proto/zip"
)

// Model is what every record carries.
type Model struct {
	// ID addresses the record.
	ID string `json:"id"`
	// Created is when the record was made, RFC 3339.
	Created string `json:"created"`
}

// Order is one order.
type Order struct {
	Model
	// Total is the order's total, in cents.
	Total int64 `json:"total"`
}

// In names an order.
type In struct {
	// ID addresses the order.
	ID string `json:"id" url:"id"`
}

// Get returns one order.
func Get(ctx context.Context, in *In) (*Order, error) { return &Order{}, nil }

// Register declares the op.
func Register(app *zip.App) { app.Get("/v1/orders/:id", Get) }
