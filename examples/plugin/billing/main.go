// The plugin. An ordinary zip app — no SDK, no schema, nothing plugin-specific
// except where it listens. Build it, and a host can load it.
//
// Its routes are TYPED ops, which is what makes a plugin worth loading: the host
// gains the routes AND their document, their MCP tools, their commands and their
// by-name call plane, because all of those are projections of the ops this
// binary registered. A plugin of untyped routes would hand the host traffic and
// nothing it can describe, address by name, or give to an agent.
package main

import (
	"context"
	"log"

	"github.com/zap-proto/zip"
)

// Invoice is one invoice.
type Invoice struct {
	ID    string `json:"id"`
	Cents int    `json:"cents"`
}

// Invoices is a page of invoices.
type Invoices struct {
	Invoices []Invoice `json:"invoices"`
}

// ListIn filters the invoice list. A GET binds its input from the URL, so
// `?limit=1` reaches the field — and the document says so.
type ListIn struct {
	Limit int `json:"limit"`
}

// ChargeIn is one charge to make.
type ChargeIn struct {
	Cents int `json:"cents" validate:"required"`
}

// Charge is the result of making one.
type Charge struct {
	Status string `json:"status"`
	Cents  int    `json:"cents"`
}

func main() {
	app := zip.New(zip.Config{AppName: "billing"})

	zip.Get(app, "/v1/billing/invoices", listInvoices)
	zip.Post(app, "/v1/billing/charge", charge)

	// zip.Addr returns the socket a host asked this process to serve on, and
	// falls back to the argument when it is run directly. That fallback is the
	// whole reason the same binary works standalone and as a plugin.
	log.Fatal(app.Listen(zip.Addr(":9654")))
}

// ListInvoices returns this org's invoices, newest first.
func listInvoices(_ context.Context, in *ListIn) (*Invoices, error) {
	all := []Invoice{{ID: "inv_1", Cents: 1200}, {ID: "inv_2", Cents: 3400}}
	if in.Limit > 0 && in.Limit < len(all) {
		all = all[:in.Limit]
	}
	return &Invoices{Invoices: all}, nil
}

// Charge bills the caller's org for the given amount.
func charge(_ context.Context, in *ChargeIn) (*Charge, error) {
	return &Charge{Status: "charged", Cents: in.Cents}, nil
}
