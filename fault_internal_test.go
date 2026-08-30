package zip

import (
	"testing"

	"github.com/zap-proto/zip/internal/jsonenc"
	"github.com/zap-proto/zip/internal/zapenc"
)

// The op-call plane's fault is a ZAP message, and in ZAP the layout IS the type:
// a field may be APPENDED and nothing else. A declared refusal body is the
// appended field, so a fleet mid-rollout has both spellings on the wire at once
// — an old callee that sends three fields, a new caller that reads four — and
// neither may lose the refusal it did carry.

// faultV1 is callFault as it was before a refusal could carry a body: the exact
// layout a callee that predates this still writes.
type faultV1 struct {
	Status int32
	Code   string
	Msg    string
}

// An OLDER callee's refusal arrives whole, with no body — not as garbage read off
// the end of a shorter message, and not as a decode failure that would collapse a
// deliberate 409 into "call failed".
func TestFault_AnOlderCalleeSendsNoBody(t *testing.T) {
	for _, old := range []faultV1{
		{Status: 409, Code: "blocked", Msg: "step 2 is blocked"},
		{Status: 402, Msg: "insufficient balance"}, // no code: the field is absent, not empty
	} {
		wire, err := zapenc.Marshal(&old)
		if err != nil {
			t.Fatalf("encode the old fault: %v", err)
		}
		he, ok := asHTTPError(remoteError(int(old.Status), "op", wire))
		if !ok {
			t.Fatalf("an older refusal did not rebuild as an HTTPError")
		}
		if he.Status != int(old.Status) || he.Code != old.Code || he.Msg != old.Msg {
			t.Fatalf("rebuilt %d/%q/%q, want %d/%q/%q", he.Status, he.Code, he.Msg, old.Status, old.Code, old.Msg)
		}
		if len(he.Body()) != 0 {
			t.Fatalf("body = %q, want none — the sender had no such field", he.Body())
		}
		if he.Error() != old.Msg {
			t.Fatalf("Error() = %q, want the message alone", he.Error())
		}
	}
}

// Bytes that arrive from ELSEWHERE are validated at the one door they come
// through: a callee that sends something other than JSON has sent NO body, rather
// than a body that fails to encode at the next hop — where the refusal a caller
// earned would become an encoding error about it.
func TestFault_ANonJSONBodyFromTheWireIsNotAdopted(t *testing.T) {
	wire, err := zapenc.Marshal(&callFault{Status: 409, Msg: "step 2 is blocked", Body: []byte("not json at all")})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	he, ok := asHTTPError(remoteError(409, "op", wire))
	if !ok {
		t.Fatal("refusal did not rebuild as an HTTPError")
	}
	if len(he.Body()) != 0 {
		t.Fatalf("body = %q, want none — those bytes are not a body", he.Body())
	}
	b, merr := jsonenc.Marshal(he)
	if merr != nil {
		t.Fatalf("the refusal no longer encodes: %v", merr)
	}
	if string(b) != `{"status":409,"error":"step 2 is blocked"}` {
		t.Fatalf("encoded as %s, want the envelope", b)
	}
}

// …and the other direction: an OLDER caller reading a new callee's refusal gets
// the three fields it knows and ignores the fourth, because the appended field
// takes a slot after them rather than moving one.
func TestFault_AnOlderCallerReadsTheThreeItKnows(t *testing.T) {
	body := []byte(`{"error":"step 2 is blocked","step":2}`)
	wire, err := zapenc.Marshal(&callFault{Status: 409, Code: "blocked", Msg: "step 2 is blocked", Body: body})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var old faultV1
	if err := zapenc.Unmarshal(wire, &old); err != nil {
		t.Fatalf("an older caller could not read it: %v", err)
	}
	if old.Status != 409 || old.Code != "blocked" || old.Msg != "step 2 is blocked" {
		t.Fatalf("older caller read %+v, want the refusal it has always read", old)
	}

	// The body itself crosses byte for byte, which is what makes it the same
	// answer the REST boundary would have written.
	var full callFault
	if err := zapenc.Unmarshal(wire, &full); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(full.Body) != string(body) {
		t.Fatalf("body crossed as %q, want %q", full.Body, body)
	}
}
