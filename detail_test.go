package zip

import (
	"encoding/json"
	"testing"
)

// TestARefusalCarriesItsDetail is the capability six subsystems were missing: a
// typed op's only way to refuse is to return an error, so a non-2xx with a shape
// could not be typed at all.
func TestARefusalCarriesItsDetail(t *testing.T) {
	err := ErrPaymentRequired("spend cap exceeded").
		With(map[string]any{"cap": 5000, "spent": 5127, "ledger": "acme"})

	raw, mErr := json.Marshal(err)
	if mErr != nil {
		t.Fatal(mErr)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != float64(402) || got["detail"] != "spend cap exceeded" {
		t.Fatalf("problem document did not survive: %v", got)
	}
	// MERGED, not nested: a reader sees one object rather than a body filed
	// under a key it has to know to look in — RFC 9457 §3.2.
	for k, want := range map[string]any{"cap": float64(5000), "spent": float64(5127), "ledger": "acme"} {
		if got[k] != want {
			t.Errorf("detail %s = %v, want %v", k, got[k], want)
		}
	}
}

// TestDetailCannotDisplaceTheDocument is the half that matters more than the
// feature. A domain field named `status` silently overwriting the refusal's
// status is how a 402 reads as a 200 — so the document's own members are
// written last and win.
func TestDetailCannotDisplaceTheDocument(t *testing.T) {
	err := ErrUnprocessable("build failed").
		With(map[string]any{"status": 200, "detail": "everything is fine", "code": "ok", "type": "https://evil"})
	err.Code = "build_failed"

	raw, _ := json.Marshal(err)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)

	for member, want := range map[string]any{
		"status": float64(422),
		"detail": "build failed",
		"code":   "build_failed",
		"type":   "about:blank",
	} {
		if got[member] != want {
			t.Errorf("%s = %v, want %v — an extension member displaced the refusal", member, got[member], want)
		}
	}
}

// TestWithMerges pins that two callers adding one fact each both land — a gate
// naming the cap and a meter naming the ledger are two facts about one refusal.
func TestWithMerges(t *testing.T) {
	e := ErrPaymentRequired("no").With(map[string]any{"cap": 1}).With(map[string]any{"ledger": "acme"})
	if e.Detail["cap"] != 1 || e.Detail["ledger"] != "acme" {
		t.Errorf("second With replaced the first: %v", e.Detail)
	}
	if ErrNotFound("x").With(nil).Detail != nil {
		t.Error("With(nil) allocated")
	}
}
