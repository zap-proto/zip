// Package fixture is a real zip service, registered every way zipdoc has to
// understand: a named handler, one built by a function that closes over a
// dependency, an inline one, explicit type arguments and inferred ones. Its
// committed zipdoc_gen.go is the assertion.
package fixture

import (
	"context"

	"github.com/zap-proto/zip"
)

//go:generate zipdoc

// ListIn asks for a page of invoices.
type ListIn struct {
	// Org whose invoices to list.
	Org string `json:"org" validate:"required"`
	// Limit caps how many come back. Zero means the server's default.
	Limit int `json:"limit,omitempty"`
	// Cursor is opaque; pass back what the last page returned.
	Cursor string `json:",omitempty"`
	// Internal is tagged "-", so it is not on the wire and not in the spec.
	Internal string `json:"-"`
	// page is unexported, so it is neither.
	page int
}

// ListOut is a page of invoices.
type ListOut struct {
	Invoices []Invoice `json:"invoices"` // Invoices, newest first.
	// Next is the cursor for the following page, empty at the end.
	Next string `json:"next"`
}

// Invoice is one billed period. Nested, so its prose has to travel too.
type Invoice struct {
	// ID is the invoice's stable identifier.
	ID string `json:"id"`
	// Cents is the amount owed, in the org's billing currency.
	Cents int64 `json:"cents"`
	Paid  bool  `json:"paid"`
}

// GetIn addresses one invoice.
type GetIn struct {
	// ID comes from the URL.
	ID string `json:"id"`
}

// VoidIn addresses the invoice to void.
type VoidIn struct {
	// ID of the invoice to void.
	ID string `json:"id"`
}

// Register wires the service. Paths are written once, here.
func Register(app *zip.App, store Store) {
	zip.Post(app, "/v1/billing/invoices", ListInvoices)
	zip.Get(app, "/v1/billing/invoices/:id", GetInvoice)
	zip.Post(app, "/v1/billing/invoices/:id/pay", payInvoice(store))

	// VoidInvoice voids an invoice that has not been paid.
	//
	// The comment above an inline handler documents it, because there is
	// nowhere else to put one.
	//
	// Response: {"ok": true}
	zip.Delete[VoidIn, VoidOut](app, "/v1/billing/invoices/:id", func(_ context.Context, in *VoidIn) (*VoidOut, error) {
		return &VoidOut{OK: in.ID != ""}, nil
	})
}

// VoidOut reports the void.
type VoidOut struct {
	// OK is true once the invoice is voided.
	OK bool `json:"ok"`
}

// ListInvoices returns every invoice for the caller's org, newest first.
//
// Results are capped by limit, and a page carries the cursor for the next one.
//
// Example: {"org": "hanzo", "limit": 25}
//
//	Response: {
//		"invoices": [{"id": "inv_1", "cents": 1200, "paid": false}],
//		"next": ""
//	}
func ListInvoices(_ context.Context, in *ListIn) (*ListOut, error) {
	return &ListOut{Invoices: []Invoice{{ID: "inv_1", Cents: 1200}}, Next: in.Cursor}, nil
}

// GetInvoice returns one invoice by id.
//
// Response: {"id": "inv_1", "cents": 1200, "paid": true}
func GetInvoice(_ context.Context, in *GetIn) (*Invoice, error) {
	return &Invoice{ID: in.ID, Cents: 1200, Paid: true}, nil
}

// Store is the dependency a handler needs and a package-level function cannot
// close over, which is why payInvoice exists at all.
type Store interface{ Pay(id string) bool }

// PayIn addresses the invoice to pay.
type PayIn struct {
	// ID of the invoice to settle.
	ID string `json:"id"`
}

// payInvoice settles an open invoice against the org's payment method and
// answers whether the balance is now clear.
//
// It is built rather than declared because it needs the store, and the returned
// closure has no declaration to carry this sentence — so the builder is where
// the sentence lives, and where zipdoc reads it.
//
// Response: {"ok": true}
func payInvoice(store Store) zip.TypedHandler[PayIn, VoidOut] {
	return func(_ context.Context, in *PayIn) (*VoidOut, error) {
		return &VoidOut{OK: store.Pay(in.ID)}, nil
	}
}
