package zipdoc

import "testing"

func TestExtract_TwoRawCallsAreBothDocumented(t *testing.T) {
	p := load(t, "twoaddr")
	if len(p.Ops) != 2 {
		var k []string
		for _, o := range p.Ops {
			k = append(k, o.Key())
		}
		t.Fatalf("ops = %d, want 2; got %v", len(p.Ops), k)
	}
	for _, want := range []string{"POST /v1/billing/invoices/:id/reminders", "POST /v1/billing/send-invoice-reminder"} {
		op := opByKey(t, p, want)
		if op.Description == "" {
			t.Errorf("%s has no prose", want)
		}
	}
}
