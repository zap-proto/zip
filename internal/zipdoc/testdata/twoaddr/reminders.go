// Package twoaddr registers one handler at two addresses with two plain Raw
// calls, which is how one handler answers at two addresses.
package twoaddr

import "github.com/zap-proto/zip"

//go:generate zipdoc

// Remind sends the invoice reminder.
func Remind(c *zip.Ctx) error { return c.NoContent(204) }

// Register declares both spellings.
func Register(app *zip.App) {
	g := app.Group("/v1/billing")
	g.Raw("POST", "/invoices/:id/reminders", Remind)
	g.Raw("POST", "/send-invoice-reminder", Remind)
}
