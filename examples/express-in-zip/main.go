// express-in-zip — the proof point: a legacy Express-shaped TypeScript
// handler running inside zip with ZERO rewrite.
//
//	app.ts (TS) --esbuild--> ES2015 JS --goja--> JSHandler --> Fiber route
//
//	go run ./examples/express-in-zip
//	curl http://localhost:8080/legacy/foo
//	curl -XPOST -d '{"x":1}' -H content-type:application/json \
//	     http://localhost:8080/legacy/bar
//	curl -XPOST -d '{"source":"40+2"}' -H content-type:application/json \
//	     http://localhost:8080/runtime/js
//
// Every route here is registered on the ZIP router. Nothing goes on
// app.Fiber() directly: a route registered there gets no *zip.Ctx, no error
// handler, no middleware and no Authorizer — it is served by the app but not
// governed by it, which is a security seam, not a shortcut. app.Fiber() is for
// reading Fiber-only APIs, not for registering routes.
package main

import (
	"context"
	_ "embed"
	"errors"
	"log"
	"strings"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
	"github.com/zap-proto/zip/js"
)

//go:embed app.ts
var appTS []byte

// setup wires esbuild -> goja -> JSHandler and mounts the legacy handler
// at /legacy/*. Returned as a *zip.App so the integration test can drive
// it via app.Fiber().Test(...) without binding a port.
func setup() (*zip.App, error) {
	// 1. Transpile the legacy TS to goja-runnable JS.
	src, err := js.TranspileToES5(appTS, js.ESOptions{
		Loader:     "ts",
		Sourcefile: "app.ts",
	})
	if err != nil {
		return nil, err
	}

	// 2. Embedded JS runtime, register the module (module.exports = fn).
	rt, err := js.NewJSRuntime(js.JSOptions{PoolSize: 8})
	if err != nil {
		return nil, err
	}
	if err := rt.LoadModule("app", string(src)); err != nil {
		return nil, err
	}

	// 3. Fiber handler from the module's exported function.
	h, err := js.JSModule(rt, "app")
	if err != nil {
		return nil, err
	}

	// 4. Mount on zip — on the ZIP router, not the Fiber one underneath it.
	//    JSModule returns a fiber.Handler, so the one-line closure below is
	//    what puts it back on the zip path: registering it with
	//    app.Fiber().All(…) would have bypassed *zip.Ctx, the error handler,
	//    the middleware installed above and the Authorizer — a route the app
	//    serves but does not govern. stripPrefix rewrites the request path so
	//    the legacy handler sees /foo, not /legacy/foo, the same path-stripping
	//    an Express sub-router does on mount.
	//
	//    It is still a WILDCARD carrying an un-rewritten JS handler, so it
	//    registers no operation: nothing under /legacy is in the OpenAPI
	//    document, is an MCP tool, or is reachable by zip.Call. That is the
	//    honest price of running the handler unchanged, and the reason to port
	//    it — a route that lands on step 5's shape gets all of them.
	app := zip.New(zip.Config{AppName: "express-in-zip"})
	app.Use(middleware.Recover(), middleware.RequestID())
	legacy := stripPrefix("/legacy", h)
	app.All("/legacy/*", func(c *zip.Ctx) error { return legacy(c.Fiber()) })

	// 5. Unified multi-language runner, as a TYPED op: :lang selects the
	//    backend and the body carries the source. Because it is an op, "run
	//    this source in this language" is in the document, is an MCP tool an
	//    agent can call, and is reachable by name from another service — none
	//    of which is true of the same handler registered on app.Fiber().
	//    zip ships the goja "js" engine in-tree; a host that imports base
	//    additionally registers pyvm/v8vm/wasmvm/starkvm here at startup — zip
	//    never imports base (see runtime/README).
	runner := js.NewRunner()
	if err := runner.Register("js", rt.Engine()); err != nil {
		return nil, err
	}
	zip.Post(app, "/runtime/:lang", runSource(runner))

	return app, nil
}

// RunIn is one evaluation: the language to run it in, and the source. `lang` is
// the path segment; the URL is the addressing authority, so it binds from there
// whatever the body says.
type RunIn struct {
	Lang   string `json:"lang"`
	Source string `json:"source" validate:"required"`
}

// RunOut is what the engine returned.
type RunOut struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// runSource dispatches the source to the engine registered for :lang. An
// unregistered language is a 404; an evaluation error is a 200 carrying the
// error string, so the caller sees the engine's own message.
func runSource(runner js.Runner) func(context.Context, *RunIn) (*RunOut, error) {
	return func(ctx context.Context, in *RunIn) (*RunOut, error) {
		res, err := runner.Run(ctx, in.Lang, []byte(in.Source))
		if err != nil {
			if errors.Is(err, js.ErrUnknownLanguage) {
				return nil, zip.ErrNotFound("unknown language")
			}
			return &RunOut{Error: err.Error()}, nil
		}
		return &RunOut{Result: res}, nil
	}
}

// stripPrefix rewrites the request path to drop prefix before delegating
// to next, mirroring Express sub-router mount semantics.
func stripPrefix(prefix string, next fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		p := strings.TrimPrefix(c.Path(), prefix)
		if p == "" {
			p = "/"
		}
		c.Path(p)
		return next(c)
	}
}

func main() {
	app, err := setup()
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(app.Listen("http://:8080"))
}
