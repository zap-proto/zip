package zip

import (
	"context"
	"encoding/json"
	"testing"
)

type tenantIn struct {
	Tenant string `json:"tenant" header:"X-Tenant"`
	Name   string `json:"name"`
}
type tenantOut struct {
	Saw string `json:"saw"`
}

// Gap 1: a typed op reads a request header WITHOUT a middleware parking it in a
// context slot — and because it declares the header, every projection sees it.
func TestHeaderParam_DeclaredHeaderBindsAndIsPublished(t *testing.T) {
	app := quiet("svc")
	Post(app, "/v1/things", func(_ context.Context, in *tenantIn) (*tenantOut, error) {
		return &tenantOut{Saw: in.Tenant}, nil
	}, WithOperationID("things"))

	// It binds off the wire, with no middleware installed anywhere.
	req := postReq("/v1/things", "application/json", []byte(`{"name":"x"}`))
	req.Header.Set("X-Tenant", "acme")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body := readAll(t, resp.Body); body != `{"saw":"acme"}` {
		t.Errorf("header did not reach the typed op: %s", body)
	}

	// And the DOCUMENT names it — the test a replacement for Bridge has to pass.
	spec := app.OpenAPISpec()
	post := spec["paths"].(map[string]map[string]any)["/v1/things"]["post"].(map[string]any)
	raw, _ := json.Marshal(post["parameters"])
	var params []map[string]any
	_ = json.Unmarshal(raw, &params)
	var found bool
	for _, p := range params {
		if p["name"] == "X-Tenant" && p["in"] == "header" {
			found = true
		}
	}
	if !found {
		t.Errorf("the document does not declare the header parameter: %s", raw)
	}
}

// Over a transport with no HTTP request the answer is honest — nothing — rather
// than a nil panic. Same shape CallerOf uses.
func TestHeaderParam_HonestOnATransportWithNoRequest(t *testing.T) {
	var in tenantIn
	bindHeaders(&in, nil) // the CLI's answer: there is no request to read
	if in.Tenant != "" {
		t.Errorf("bindHeaders invented a value with no request: %q", in.Tenant)
	}
}
