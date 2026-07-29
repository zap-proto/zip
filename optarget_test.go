package zip_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// groupIn addresses one thing under a group.
type groupIn struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

type groupOut struct {
	ID   string `json:"id"`
	Note string `json:"note"`
	Seen string `json:"seen"`
}

func echoGroup(_ context.Context, in *groupIn) (*groupOut, error) {
	return &groupOut{ID: in.ID, Note: in.Note}, nil
}

func call2(t *testing.T, a *zip.App, method, path, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// A typed op declared on a Group carries the group's prefix — in the ROUTE it
// answers on and in the identity every projection reads. Before this, zip.Get
// took the *App only, so an app built on groups had to spell its prefix out per
// route to have typed ops at all: friction that made the untyped handler, which
// does inherit the prefix, the path of least resistance.
func TestOpTarget_GroupPrefixIsPartOfTheOp(t *testing.T) {
	a := zip.New(zip.Config{AppName: "g", DisableStartupMessage: true})
	v1 := a.Group("/v1")
	things := v1.Group("/things")

	zip.Get(things, "/:id", echoGroup)
	zip.Post(v1, "/direct", echoGroup)

	// The route answers under the composed prefix.
	if code, body := call2(t, a, "GET", "/v1/things/abc?note=hi", ""); code != 200 ||
		!strings.Contains(body, `"id":"abc"`) || !strings.Contains(body, `"note":"hi"`) {
		t.Fatalf("GET /v1/things/abc = %d %s", code, body)
	}
	if code, _ := call2(t, a, "GET", "/things/abc", ""); code != 404 {
		t.Errorf("GET /things/abc = %d, want 404 — the prefix is part of the route", code)
	}

	// And the identity every projection reads is the whole path.
	paths := map[string]bool{}
	for _, c := range a.Commands() {
		paths[c.Path] = true
	}
	for _, want := range []string{"/v1/things/:id", "/v1/direct"} {
		if !paths[want] {
			t.Errorf("no op at %q; ops are %v", want, keysOf(anyMap(paths)))
		}
	}
	spec, _ := a.OpenAPISpec()["paths"].(map[string]map[string]any)
	if _, ok := spec["/v1/things/{id}"]; !ok {
		t.Errorf("document has no /v1/things/{id}; paths = %v", keysOf(anyMap2(spec)))
	}
}

// A typed op and an untyped one declared on the SAME group land on the same
// prefix. zip composes the typed op's path itself, so this is the test that says
// its composition is the router's composition and not a second rule beside it.
func TestOpTarget_TypedAndUntypedComposeAlike(t *testing.T) {
	a := zip.New(zip.Config{AppName: "g", DisableStartupMessage: true})
	g := a.Group("/v1/mix")
	g.Get("/untyped", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"kind": "untyped"}) })
	zip.Get(a.Group("/v1/mix"), "/typed", echoGroup)

	if code, body := call2(t, a, "GET", "/v1/mix/untyped", ""); code != 200 || !strings.Contains(body, "untyped") {
		t.Errorf("untyped leaf = %d %s", code, body)
	}
	if code, _ := call2(t, a, "GET", "/v1/mix/typed", ""); code != 200 {
		t.Errorf("typed leaf = %d, want 200 at the same prefix", code)
	}
}

// With() middleware composes around a typed op too. Dropping it silently would
// be a hole rather than an inconvenience: With is where the gates go.
func TestOpTarget_WithWrapsATypedOp(t *testing.T) {
	a := zip.New(zip.Config{AppName: "g", DisableStartupMessage: true})
	gate := func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			if c.Header("X-Pass") != "yes" {
				return zip.ErrForbidden("gated")
			}
			c.SetHeader("X-Gate", "ran")
			return next(c)
		}
	}
	zip.Get(a.With(gate), "/v1/gated/:id", echoGroup)

	if code, body := call2(t, a, "GET", "/v1/gated/x", ""); code != 403 {
		t.Fatalf("ungated request = %d %s, want 403 — the middleware did not run", code, body)
	}

	req, _ := http.NewRequest("GET", "/v1/gated/x", nil)
	req.Header.Set("X-Pass", "yes")
	resp, err := a.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("gated-through request = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Gate") != "ran" {
		t.Errorf("X-Gate = %q, want the middleware's mark", resp.Header.Get("X-Gate"))
	}
	// It is still one op, with the whole path.
	if cmds := a.Commands(); len(cmds) != 1 || cmds[0].Path != "/v1/gated/:id" {
		t.Errorf("commands = %+v, want one op at /v1/gated/:id", cmds)
	}
}

func anyMap(m map[string]bool) map[string]any {
	out := map[string]any{}
	for k := range m {
		out[k] = nil
	}
	return out
}

func anyMap2(m map[string]map[string]any) map[string]any {
	out := map[string]any{}
	for k := range m {
		out[k] = nil
	}
	return out
}
