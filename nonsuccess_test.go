package zip

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type lookupIn struct {
	ID string `json:"id"`
}
type lookupOut struct {
	Found      bool     `json:"found"`
	Suggestion []string `json:"suggestion,omitempty"`
}

// A 404 carrying what was searched for — a typed body the error envelope
// cannot express.
func (l *lookupOut) StatusCode() int {
	if l.Found {
		return 200
	}
	return 404
}

// Gap 8: an op declares a non-2xx and answers it with its own typed body.
func TestNonSuccess_TypedBodyOnADeclaredNon2xx(t *testing.T) {
	app := quiet("svc")
	Post(app, "/v1/lookup", func(_ context.Context, in *lookupIn) (*lookupOut, error) {
		if in.ID == "known" {
			return &lookupOut{Found: true}, nil
		}
		return &lookupOut{Suggestion: []string{"known"}}, nil
	}, WithOperationID("lookup"), WithStatus(200, 404))

	if code, body := postJSON(t, app, "/v1/lookup", `{"id":"known"}`); code != 200 || !strings.Contains(body, `"found":true`) {
		t.Errorf("found case: %d %s", code, body)
	}
	code, body := postJSON(t, app, "/v1/lookup", `{"id":"missing"}`)
	if code != 404 {
		t.Errorf("missing case answered %d, want 404", code)
	}
	// The typed body reached the wire, not the error envelope.
	if !strings.Contains(body, `"suggestion"`) {
		t.Errorf("the 404 did not carry the op's own body: %s", body)
	}
	if strings.Contains(body, `"error"`) {
		t.Errorf("the 404 rendered the error envelope instead of the typed body: %s", body)
	}

	// And the document describes it under 404, with the Out schema.
	spec := app.OpenAPISpec()
	post := spec["paths"].(map[string]map[string]any)["/v1/lookup"]["post"].(map[string]any)
	raw, _ := json.Marshal(post["responses"])
	for _, want := range []string{`"200"`, `"404"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("document omits %s: %s", want, raw)
		}
	}
}

// Gap 9: a verbatim passthrough is already expressible — json.RawMessage as the
// input keeps every unnamed field, so forwarding bytes to an upstream that owns
// the contract needs no raw-request accessor.
type echoOut struct {
	Echo json.RawMessage `json:"echo"`
}

func TestVerbatimBody_NeedsNoRawRequestAccessor(t *testing.T) {
	app := quiet("svc")
	Post(app, "/v1/pass", func(_ context.Context, in *json.RawMessage) (*echoOut, error) {
		return &echoOut{Echo: *in}, nil
	}, WithOperationID("pass"))

	_, body := postJSON(t, app, "/v1/pass", `{"known":1,"unnamed":{"deep":true},"extra":[1,2]}`)
	for _, want := range []string{"unnamed", "deep", "extra"} {
		if !strings.Contains(body, want) {
			t.Errorf("re-marshalling dropped %q: %s", want, body)
		}
	}
}
