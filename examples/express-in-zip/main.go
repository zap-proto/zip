// express-in-zip — the proof point: a legacy Express-shaped TypeScript
// handler running inside zip with ZERO rewrite.
//
//	app.ts (TS) --esbuild--> ES2015 JS --goja--> JSHandler --> Fiber route
//
//	go run ./examples/express-in-zip
//	curl http://localhost:8080/legacy/foo
//	curl -XPOST -d '{"x":1}' -H content-type:application/json \
//	     http://localhost:8080/legacy/bar
package main

import (
	_ "embed"
	"errors"
	"log"
	"strings"

	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
	"github.com/zap-proto/zip/runtime"
)

//go:embed app.ts
var appTS []byte

// setup wires esbuild -> goja -> JSHandler and mounts the legacy handler
// at /legacy/*. Returned as a *zip.App so the integration test can drive
// it via app.Fiber().Test(...) without binding a port.
func setup() (*zip.App, error) {
	// 1. Transpile the legacy TS to goja-runnable JS.
	js, err := runtime.TranspileToES5(appTS, runtime.ESOptions{
		Loader:     "ts",
		Sourcefile: "app.ts",
	})
	if err != nil {
		return nil, err
	}

	// 2. Embedded JS runtime, register the module (module.exports = fn).
	rt, err := runtime.NewJSRuntime(runtime.JSOptions{PoolSize: 8})
	if err != nil {
		return nil, err
	}
	if err := rt.LoadModule("app", string(js)); err != nil {
		return nil, err
	}

	// 3. Fiber handler from the module's exported function.
	h, err := runtime.JSModule(rt, "app")
	if err != nil {
		return nil, err
	}

	// 4. Mount on zip. JSModule returns a fiber.Handler, so it goes on
	//    the underlying Fiber router via app.Fiber(); native zip routes
	//    sit alongside it on the same App. stripPrefix rewrites the
	//    request path so the legacy handler sees /foo, not /legacy/foo —
	//    the same path-stripping an Express sub-router does on mount.
	app := zip.New(zip.Config{AppName: "express-in-zip"})
	app.Use(middleware.Recover(), middleware.RequestID())
	app.Fiber().All("/legacy/*", stripPrefix("/legacy", h))

	// 5. Unified multi-language runner. The request body is the source,
	//    :lang selects the backend. zip ships the goja "js" engine in-tree;
	//    a host that imports base additionally registers pyvm/v8vm/wasmvm/
	//    starkvm here at startup — zip never imports base (see runtime/README).
	runner := runtime.NewRunner()
	if err := runner.Register("js", rt.Engine()); err != nil {
		return nil, err
	}
	app.Fiber().Post("/runtime/:lang", runtimeHandler(runner))

	return app, nil
}

// runtimeHandler reads the request body as source, dispatches it to the
// engine registered for :lang, and returns {result, error} as JSON. An
// unregistered language is a 404; an evaluation error is a 200 carrying
// the error string in the body so the caller sees the engine's message.
func runtimeHandler(runner runtime.Runner) fiber.Handler {
	return func(c fiber.Ctx) error {
		lang := c.Params("lang")
		res, err := runner.Run(c.Context(), lang, c.Body())
		if err != nil {
			if errors.Is(err, runtime.ErrUnknownLanguage) {
				return c.Status(fiber.StatusNotFound).
					JSON(fiber.Map{"error": "unknown language"})
			}
			return c.JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"result": res})
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
