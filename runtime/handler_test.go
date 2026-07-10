package runtime

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/fiber/v3"
)

// TestJSHandler_EndToEnd: an Express-shaped handler defined as a global
// JS function, mounted via JSHandler, exercised over a real HTTP
// roundtrip through Fiber.
func TestJSHandler_EndToEnd(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 2})
	_, err := rt.Eval(`
		function handler(req, res) {
			res.status(201).set("X-From", "goja").json({
				ok: true,
				method: req.method,
				path: req.path,
				body: req.body,
			});
		}
	`)
	if err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Post("/echo", JSHandler(rt, "handler"))

	req := httptest.NewRequest("POST", "/echo", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if got := resp.Header.Get("X-From"); got != "goja" {
		t.Fatalf("X-From = %q, want goja", got)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	for _, want := range []string{`"ok":true`, `"method":"POST"`, `"path":"/echo"`, `"x":1`} {
		if !strings.Contains(bs, want) {
			t.Fatalf("body %q missing %q", bs, want)
		}
	}
}

// TestJSModule_EndToEnd: a CommonJS module whose exports IS the handler,
// loaded from esbuild-transpiled TS, mounted via JSModule.
func TestJSModule_EndToEnd(t *testing.T) {
	rt, _ := NewJSRuntime(JSOptions{PoolSize: 1})

	tsSrc := []byte(`
		module.exports = function (req: any, res: any) {
			res.json({ ok: true, path: req.path });
		};
	`)
	es5, err := TranspileToES5(tsSrc, ESOptions{Loader: "ts"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadModule("app", string(es5)); err != nil {
		t.Fatal(err)
	}

	h, err := JSModule(rt, "app")
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Get("/m", h)

	resp, err := app.Test(httptest.NewRequest("GET", "/m", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"path":"/m"`) {
		t.Fatalf("body %q missing path", body)
	}
}
