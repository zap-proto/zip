package zip

import (
	"context"
	"strings"
	"testing"
)

type createIn struct {
	Name string `json:"name"`
}
type createOut struct {
	ID      string `json:"id"`
	Existed bool   `json:"existed"`
}

// The answer states its own status. No mutable slot, no side channel.
func (c *createOut) StatusCode() int {
	if c.Existed {
		return 200
	}
	return 201
}

// Gap 2: a route with two legitimate success codes is declarable, the handler
// selects per request, and BOTH reach the document.
func TestMultiStatus_BothCodesAreDeclaredAndSelectable(t *testing.T) {
	app := quiet("svc")
	Post(app, "/v1/things", func(_ context.Context, in *createIn) (*createOut, error) {
		return &createOut{ID: in.Name, Existed: in.Name == "old"}, nil
	}, WithStatus(201, 200), WithOperationID("create"))

	// Both codes are in the document — the visibility constraint.
	spec := app.OpenAPISpec()
	post := spec["paths"].(map[string]map[string]any)["/v1/things"]["post"].(map[string]any)
	resp := post["responses"].(map[string]any)
	for _, want := range []string{"200", "201"} {
		if _, ok := resp[want]; !ok {
			t.Errorf("document omits declared status %s: %v", want, keysOfSchemas(resp))
		}
	}

	// And the handler selects per request.
	if code, _ := postJSON(t, app, "/v1/things", `{"name":"new"}`); code != 201 {
		t.Errorf("new thing answered %d, want 201", code)
	}
	if code, _ := postJSON(t, app, "/v1/things", `{"name":"old"}`); code != 200 {
		t.Errorf("existing thing answered %d, want 200", code)
	}
}

type sneakOut struct{}

func (sneakOut) StatusCode() int { return 202 }

// A code the op did not declare is refused rather than written: the document
// publishes the declared set, so anything else tells the caller one thing and
// sends another.
func TestMultiStatus_UndeclaredCodeIsRefused(t *testing.T) {
	app := quiet("svc")
	Post(app, "/v1/sneak", func(_ context.Context, in *createIn) (*sneakOut, error) {
		return &sneakOut{}, nil
	}, WithStatus(200), WithOperationID("sneak"))

	code, body := postJSON(t, app, "/v1/sneak", `{"name":"x"}`)
	if code == 202 {
		t.Fatal("an undeclared status reached the wire")
	}
	if code != 500 {
		t.Errorf("undeclared status answered %d, want 500", code)
	}
	for _, want := range []string{"202", "WithStatus"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not name %q: %s", want, body)
		}
	}
}

func postJSON(t *testing.T, a *App, path, body string) (int, string) {
	t.Helper()
	req := postReq(path, "application/json", []byte(body))
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, readAll(t, resp.Body)
}
