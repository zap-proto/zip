// Package alias registers ops on a router named by an alias, which is how a
// host that re-exports the type spells it: the host's own package says
// `type Router = *zip.App`, and every plugin mount takes a Router.
package alias

import (
	"context"

	"github.com/zap-proto/zip"
)

//go:generate zipdoc

// Router is this host's name for the app a plugin mounts on.
type Router = *zip.App

// Branch is this host's name for a confined part of it.
type Branch = *zip.Group

// AskIn is the question.
type AskIn struct {
	// Prompt is what to answer.
	Prompt string `json:"prompt"`
}

// AskOut is the answer.
type AskOut struct {
	// Reply is the answer.
	Reply string `json:"reply"`
}

// Ask answers a question.
func Ask(ctx context.Context, in *AskIn) (*AskOut, error) { return &AskOut{Reply: in.Prompt}, nil }

// Mount declares one op on an aliased router and one on an aliased group.
func Mount(r Router) {
	r.Post("/v1/ask", Ask)

	var b Branch = r.Group("/v1/ask/history")
	b.Post("/replay", Ask)

	var plain = r.Group("/v1/ask/audit")
	plain.Post("/entries", Ask)
}
