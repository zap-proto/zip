package zip_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

type invIn struct {
	Limit int    `json:"limit"`
	Org   string `json:"org" validate:"required"`
}
type invOut struct {
	Invoices []string `json:"invoices"`
}

// TestDoc_ReachesTheSpec proves an extracted doc comment becomes the operation's
// description, its fields' descriptions, and its request/response examples —
// i.e. that the comment IS the spec rather than something written twice.
func TestDoc_ReachesTheSpec(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})

	// What cmd/zipdoc emits for this operation.
	zip.Describe("POST /v1/billing/invoices", zip.Doc{
		Description: "ListInvoices returns every invoice for the caller's org, newest first. Results are capped by limit.",
		Fields: map[string]string{
			"invIn.limit":     "Maximum invoices to return.",
			"invIn.org":       "Org whose invoices to list.",
			"invOut.invoices": "Invoice ids, newest first.",
		},
		Example:  json.RawMessage(`{"limit":25,"org":"hanzo"}`),
		Response: json.RawMessage(`{"invoices":["inv_2","inv_1"]}`),
	})
	zip.Post(app, "/v1/billing/invoices", func(_ context.Context, in *invIn) (*invOut, error) {
		return &invOut{}, nil
	})

	spec := app.OpenAPISpec()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		`newest first`,                 // description
		`Maximum invoices to return.`,  // In field doc
		`Invoice ids, newest first.`,   // Out field doc
		`"limit":25`,                   // request example
		`"invoices":["inv_2","inv_1"]`, // response example
	} {
		if !strings.Contains(got, want) {
			t.Errorf("spec is missing %q", want)
		}
	}

	// The summary falls back to the comment's first sentence, which Go's own
	// convention already makes a summary — so nobody writes it twice.
	ops := spec["paths"].(map[string]map[string]any)["/v1/billing/invoices"]
	post := ops["post"].(map[string]any)
	if s, _ := post["summary"].(string); !strings.HasPrefix(s, "ListInvoices returns") || strings.Contains(s, "capped by limit") {
		t.Errorf("summary = %q, want just the first sentence", s)
	}
}
