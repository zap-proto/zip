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
	if got["status"] != float64(402) || got["error"] != "spend cap exceeded" {
		t.Fatalf("envelope did not survive: %v", got)
	}
	// MERGED, not nested: a reader sees one object rather than a body filed
	// under a key it has to know to look in.
	for k, want := range map[string]any{"cap": float64(5000), "spent": float64(5127), "ledger": "acme"} {
		if got[k] != want {
			t.Errorf("detail %s = %v, want %v", k, got[k], want)
		}
	}
}

// TestDetailCannotDisplaceTheEnvelope is the half that matters more than the
// feature. A domain field named `status` silently overwriting the refusal's
// status is how a 402 reads as a 200 — so the envelope is written last and wins.
func TestDetailCannotDisplaceTheEnvelope(t *testing.T) {
	err := ErrUnprocessable("build failed").
		With(map[string]any{"status": 200, "error": "everything is fine", "code": "ok"})
	err.Code = "build_failed"

	raw, _ := json.Marshal(err)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)

	if got["status"] != float64(422) {
		t.Errorf("status = %v, want 422 — a detail key displaced the refusal", got["status"])
	}
	if got["error"] != "build failed" {
		t.Errorf("error = %v, want the refusal's own message", got["error"])
	}
	if got["code"] != "build_failed" {
		t.Errorf("code = %v, want the refusal's own code", got["code"])
	}
}

// TestNoDetailIsTheShapeItAlwaysWas pins that this is ADDITIVE. Twelve
// repositories render errors through this type; a refusal carrying no detail
// must marshal byte-identically to what they read before.
func TestNoDetailIsTheShapeItAlwaysWas(t *testing.T) {
	raw, _ := json.Marshal(&HTTPError{Status: 404, Code: "absent", Msg: "no such thing"})
	if string(raw) != `{"status":404,"code":"absent","error":"no such thing"}` {
		t.Errorf("shape moved: %s", raw)
	}
	bare, _ := json.Marshal(&HTTPError{Status: 500, Msg: "boom"})
	if string(bare) != `{"status":500,"error":"boom"}` {
		t.Errorf("shape moved: %s", bare)
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
