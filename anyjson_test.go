package zip_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// relayOut carries another runtime's answer through verbatim. What comes back is
// whatever that runtime sent: an object, an array, a number, null.
type relayOut struct {
	// Result is the upstream body, untouched.
	Result json.RawMessage `json:"result"`
	// Blob is an ordinary []byte, which encoding/json writes as a base64
	// string. It is NOT an array of integers, and it is not a marshaler.
	Blob []byte `json:"blob"`
	// At is a timestamp. time.Time writes itself as an RFC 3339 string, so its
	// FIELDS are not its wire shape either.
	At time.Time `json:"at"`
}

type relayIn struct {
	ID string `json:"id"`
}

func relay(_ context.Context, _ *relayIn) (*relayOut, error) {
	return &relayOut{Result: json.RawMessage(`[1,2]`)}, nil
}

// A type that says what it is on the wire — MarshalJSON — is not described by
// its fields, and json.RawMessage is the case that matters: it is a []byte whose
// MarshalJSON emits raw JSON, so a relay that demonstrably returns arrays and
// null was published as `{"type":"object","properties":{}}` in openapi.yaml and
// in every SDK generated from it.
//
// The rule is about the MARSHALER, so it has to be read BEFORE the []byte rule
// and before the struct rule — being a struct was never what made it true.
func TestAnyJSON_ACustomMarshalerIsNotItsFields(t *testing.T) {
	app := zip.New(zip.Config{AppName: "relay", DisableStartupMessage: true})
	zip.Describe("GET /v1/relay/:id", zip.Doc{
		Description: "Relay forwards one call.",
		Fields:      map[string]string{"relayOut.result": "The upstream body, untouched."},
	})
	zip.Get(app, "/v1/relay/:id", relay)

	props := propsOf(t, app, "/v1/relay/{id}", "get")

	result, _ := props["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no `result` property: %v", props)
	}
	if _, typed := result["type"]; typed {
		t.Errorf("result = %v, want an unconstrained schema — a RawMessage is any JSON", result)
	}
	// Unconstrained is not undocumented: the field's prose still lands on it.
	if result["description"] != "The upstream body, untouched." {
		t.Errorf("result lost its description: %v", result)
	}

	// A plain []byte is not a marshaler. encoding/json writes it as a base64
	// string, so that is what the document says — not an array of integers.
	if blob, _ := props["blob"].(map[string]any); blob["type"] != "string" {
		t.Errorf("blob = %v, want a base64 string", blob)
	}

	// time.Time IS a marshaler, and the one whose output shape is documented:
	// RFC 3339, which OpenAPI spells `format: date-time`.
	at, _ := props["at"].(map[string]any)
	if at["type"] != "string" || at["format"] != "date-time" {
		t.Errorf("at = %v, want an RFC 3339 string", at)
	}
}

// And the wire is what the schema now admits: the relay hands back an array and
// a null through the same op, unchanged.
func TestAnyJSON_RelayIsVerbatim(t *testing.T) {
	app := zip.New(zip.Config{AppName: "relay2", DisableStartupMessage: true})
	zip.Get(app, "/v2/relay/:id", func(_ context.Context, in *relayIn) (*json.RawMessage, error) {
		out := json.RawMessage(`[1,2]`)
		if in.ID == "null" {
			out = json.RawMessage(`null`)
		}
		return &out, nil
	})
	for id, want := range map[string]string{"x": "[1,2]", "null": "null"} {
		if code, body := call2(t, app, "GET", "/v2/relay/"+id, ""); code != 200 || body != want {
			t.Errorf("GET /v2/relay/%s = %d %s, want 200 %s", id, code, body, want)
		}
	}

	// An op whose whole Out is a RawMessage says so too — the response schema is
	// unconstrained, not an array of integers.
	paths, _ := app.OpenAPISpec()["paths"].(map[string]map[string]any)
	op, _ := paths["/v2/relay/{id}"]["get"].(map[string]any)
	resp, _ := op["responses"].(map[string]any)["200"].(map[string]any)
	content, _ := resp["content"].(map[string]any)["application/json"].(map[string]any)
	schema, _ := content["schema"].(map[string]any)
	if _, typed := schema["type"]; typed {
		t.Errorf("response schema = %v, want unconstrained", schema)
	}
}

// propsOf is the response schema's properties for one op, resolved through the
// $ref the document uses to name a type once.
func propsOf(t *testing.T, a *zip.App, path, method string) map[string]any {
	t.Helper()
	spec := a.OpenAPISpec()
	paths, _ := spec["paths"].(map[string]map[string]any)
	op, _ := paths[path][method].(map[string]any)
	if op == nil {
		t.Fatalf("no %s %s in spec", method, path)
	}
	resp, _ := op["responses"].(map[string]any)["200"].(map[string]any)
	content, _ := resp["content"].(map[string]any)["application/json"].(map[string]any)
	schema, _ := content["schema"].(map[string]any)
	if ref, ok := schema["$ref"].(string); ok {
		defs, _ := spec["components"].(map[string]any)["schemas"].(map[string]any)
		schema, _ = defs[ref[len("#/components/schemas/"):]].(map[string]any)
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("no properties in %v", schema)
	}
	return props
}
