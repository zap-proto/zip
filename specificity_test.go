package zip_test

import (
	"net/http"
	"strings"
	"testing"

	zip "github.com/zap-proto/zip"
)

// These tests pin the route-precedence CONTRACT zip inherits from the
// zap-proto/fiber fork (ServeMux-1.22 semantics): the most specific pattern
// wins regardless of registration order, ambiguous overlaps panic at
// registration, and middleware keeps declaration order. They test the zip
// surface, not the engine — if the engine ever changes, these must still pass.

func specApp() *zip.App {
	return zip.New(zip.Config{AppName: "spec-test", DisableStartupMessage: true})
}

func mark(s string) zip.Handler {
	return func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"via": s}) }
}

func hit(t *testing.T, app *zip.App, path string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", path, nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%s): %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d, want 200", path, resp.StatusCode)
	}
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

// The cloud order-int case, inverted: the wildcard is registered FIRST and the
// static route must still win.
func TestSpecificity_StaticBeatsEarlierWildcard(t *testing.T) {
	app := specApp()
	app.Get("/v1/iam/*", mark("wildcard"))
	app.Get("/v1/iam/keys", mark("static"))

	if got := hit(t, app, "/v1/iam/keys"); !strings.Contains(got, "static") {
		t.Fatalf("/v1/iam/keys hit %s, want static", got)
	}
	if got := hit(t, app, "/v1/iam/anything-else"); !strings.Contains(got, "wildcard") {
		t.Fatalf("/v1/iam/anything-else hit %s, want wildcard", got)
	}
}

// Full ladder registered most-generic-first: static beats :param beats *.
func TestSpecificity_Ladder(t *testing.T) {
	app := specApp()
	app.Get("/users/*", mark("wildcard"))
	app.Get("/users/:id", mark("param"))
	app.Get("/users/me", mark("static"))

	if got := hit(t, app, "/users/me"); !strings.Contains(got, "static") {
		t.Fatalf("/users/me hit %s, want static", got)
	}
	if got := hit(t, app, "/users/42"); !strings.Contains(got, "param") {
		t.Fatalf("/users/42 hit %s, want param", got)
	}
	if got := hit(t, app, "/users/a/b"); !strings.Contains(got, "wildcard") {
		t.Fatalf("/users/a/b hit %s, want wildcard", got)
	}
}

// A deeper static pattern beats a shallower wildcard registered before it.
func TestSpecificity_DeeperStaticBeatsShallowWildcard(t *testing.T) {
	app := specApp()
	app.Get("/a/*", mark("wildcard"))
	app.Get("/a/b/c", mark("deep-static"))

	if got := hit(t, app, "/a/b/c"); !strings.Contains(got, "deep-static") {
		t.Fatalf("/a/b/c hit %s, want deep-static", got)
	}
	if got := hit(t, app, "/a/x"); !strings.Contains(got, "wildcard") {
		t.Fatalf("/a/x hit %s, want wildcard", got)
	}
}

// Two distinct, equally specific, unconstrained patterns are an ambiguous
// overlap: registration must panic loudly, never silently shadow.
func TestSpecificity_AmbiguousConflictPanics(t *testing.T) {
	app := specApp()
	app.Get("/x/:id", mark("id"))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("registering /x/:name after /x/:id did not panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "route conflict") {
			t.Fatalf("panic %q, want it to name the route conflict", r)
		}
	}()
	app.Get("/x/:name", mark("name"))
}

// Same pattern, different methods: no conflict — method stacks are independent.
func TestSpecificity_MethodsIndependent(t *testing.T) {
	app := specApp()
	app.Get("/y/:id", mark("get"))
	app.Post("/y/:id", mark("post")) // must not panic
	if got := hit(t, app, "/y/1"); !strings.Contains(got, "get") {
		t.Fatalf("/y/1 hit %s, want get", got)
	}
}

// Middleware keeps declaration order and runs before the matched endpoint,
// even though the endpoint is more specific than anything: sorting must never
// hoist an endpoint above middleware (the use-barrier property).
func TestSpecificity_MiddlewareOrderPreserved(t *testing.T) {
	app := specApp()
	var order []string
	app.Use(func(c *zip.Ctx) error { order = append(order, "mw1"); return c.Next() })
	app.Use(func(c *zip.Ctx) error { order = append(order, "mw2"); return c.Next() })
	app.Get("/z", func(c *zip.Ctx) error {
		order = append(order, "handler")
		return c.JSON(200, map[string]string{"via": "z"})
	})

	_ = hit(t, app, "/z")
	if strings.Join(order, ",") != "mw1,mw2,handler" {
		t.Fatalf("execution order %v, want [mw1 mw2 handler]", order)
	}
}
