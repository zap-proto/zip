// Package nested registers through a group: the verb methods are generic
// methods on a concrete type, so the declarations carry no type arguments and
// the handler is the bound method. zipdoc has to read that shape, because it is
// the shape a service is written in.
package nested

import (
	"context"

	"github.com/zap-proto/zip"
)

//go:generate zipdoc

// OpenIn asks for a new account.
type OpenIn struct {
	// Name the account answers to.
	Name string `json:"name" validate:"required"`
}

// Account is one account.
type Account struct {
	// ID addresses the account.
	ID string `json:"id"`
	// Name it answers to.
	Name string `json:"name"`
}

// Ledger is what an account holds.
type Ledger struct {
	// Cents held.
	Cents int64 `json:"cents"`
}

// API serves accounts.
type API struct{}

// Open opens an account for the caller's org.
func (a *API) Open(ctx context.Context, in *OpenIn) (*Account, error) {
	return &Account{ID: "acct_1", Name: in.Name}, nil
}

// Read returns one account.
func (a *API) Read(ctx context.Context, in *struct{}) (*Account, error) {
	return &Account{ID: "acct_1"}, nil
}

// Balance returns what the account holds.
func (a *API) Balance(ctx context.Context, in *struct{}) (*Ledger, error) {
	return &Ledger{}, nil
}

// Register declares the accounts surface.
func Register(app *zip.App, api *API) {
	accounts := app.Group("/v1/accounts").Tag("accounts")

	// A metadata chain follows the declaration; the registration is the inner
	// call, and zipdoc has to find it there.
	accounts.Post("/open", api.Open).ID("accounts.open").Summary("Open an account")
	accounts.Get("/:id", api.Read)

	// A nested group composes its prefix.
	accounts.Group("/:id").Get("/ledger", api.Balance)
}
