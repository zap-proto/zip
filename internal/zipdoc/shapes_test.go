package zipdoc

import "testing"

// Prose a type's fields carry has to reach the key the document reads, however
// the type reaches the op: through a generic, a type defined over another, an
// embedded struct, or one of zip's kinds.
func TestExtract_ShapesReachTheirProse(t *testing.T) {
	const base = "github.com/zap-proto/zip/internal/zipdoc/testdata/"
	for pkg, ops := range map[string]map[string]map[string]string{
		"generic": {
			"GET /v1/stores/:owner": {
				"Envelope[[]" + base + "generic.Store].status": "Status is ok or error.",
				"Envelope[[]" + base + "generic.Store].data":   "Data is what the op answers.",
				"Store.name": "Name is the store's name.",
			},
			"GET /v1/store/:owner": {
				"Envelope[" + base + "generic.Store].status": "Status is ok or error.",
			},
		},
		"defined": {
			"GET /v1/filings/:id":        {"legalFiling.name": "Name is the filing's title."},
			"GET /v1/filings/:id/docket": {"docket.court": "Court is the court that holds the record."},
		},
		"embedded": {
			"GET /v1/orders/:id": {
				"Model.id":      "ID addresses the record.",
				"Model.created": "Created is when the record was made, RFC 3339.",
				"Order.total":   "Total is the order's total, in cents.",
			},
		},
		"kinds": {
			"POST /v1/stream": {"Chunk.text": "Text is the next piece of the completion.", "Prompt.stream": "Stream asks for events rather than one answer."},
			"POST /v1/either": {"Chunk.text": "Text is the next piece of the completion.", "Done.answer": "Answer is the whole completion."},
			"POST /v1/relay":  {"Upstream.model": "Model is the model the upstream ran."},
			"POST /v1/ingest": {"Event.kind": "Kind names the event.", "Batch.events": "Events are the events, oldest first."},
		},
	} {
		p := load(t, pkg)
		for key, want := range ops {
			op := opByKey(t, p, key)
			for field, prose := range want {
				if got := op.Fields[field]; got != prose {
					t.Errorf("%s %s: Fields[%q] = %q, want %q", pkg, key, field, got, prose)
				}
			}
			// zip's own kinds are not wire shapes, so nothing of theirs is filed.
			for field := range op.Fields {
				for _, kind := range []string{"Sse[", "Or[", "Verbatim[", "Body."} {
					if len(field) >= len(kind) && field[:len(kind)] == kind {
						t.Errorf("%s %s: filed zip's own %q", pkg, key, field)
					}
				}
			}
		}
	}
}
