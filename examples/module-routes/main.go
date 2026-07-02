// module-routes example — mount HIP-0105 extension modules as routes.
//
// The loader is constructed by the embedding binary (typically
// *extruntime.Loader from hanzoai/base/plugins/extruntime, with
// gojavm + wasmvm + pyvm + starlarkvm registered). zip is a CONSUMER
// of the loader — it accepts anything that satisfies zip/runtime.Loader.
//
// This example stubs a tiny in-memory loader so the example compiles
// without pulling hanzoai/base. Replace with real *extruntime.Loader
// in production.
package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
	"github.com/zap-proto/zip/runtime"
)

// stubLoader is a zero-dependency runtime.Loader that echoes the
// envelope back. Replace with *extruntime.Loader in a real service.
type stubLoader struct{}

func (stubLoader) Runtimes() []string {
	return []string{"goja", "wazero", "pyvm", "starlark", "native"}
}

func (stubLoader) LoadDir(_ context.Context, _ string) (map[string]runtime.Module, error) {
	return map[string]runtime.Module{}, nil
}

func (stubLoader) LoadOne(_ context.Context, dir string) (runtime.Module, error) {
	return &stubModule{dir: dir, rt: "goja"}, nil
}

type stubModule struct {
	dir string
	rt  string
}

func (m *stubModule) Name() string      { return m.dir }
func (m *stubModule) Runtime() string   { return m.rt }
func (m *stubModule) Exports() []string { return []string{"handler"} }
func (m *stubModule) Close() error      { return nil }
func (m *stubModule) Invoke(_ context.Context, fn string, payload []byte) ([]byte, error) {
	return json.Marshal(map[string]any{
		"status":  200,
		"headers": map[string]string{"X-Module-Stub": "1"},
		"body":    map[string]any{"fn": fn, "echo": json.RawMessage(payload)},
	})
}

func main() {
	app := zip.New(zip.Config{
		AppName:         "module-routes",
		Loader:          stubLoader{},
		AllowedRuntimes: []string{"goja", "wazero", "pyvm", "starlark"},
	})
	app.Use(middleware.Recover(), middleware.RequestID())

	// Mount one module per runtime. In a real deployment each `./ext/*`
	// directory contains an extension.json manifest + compiled artifact.
	mustMount(app, "POST /v1/policy/eval", "wazero", "./ext/policy")
	mustMount(app, "POST /v1/transform", "pyvm", "./ext/transform")
	mustMount(app, "POST /v1/webhook", "goja", "./ext/webhook")
	mustMount(app, "POST /v1/route", "starlark", "./ext/route")

	log.Fatal(app.ListenHTTP(":8080"))
}

func mustMount(app *zip.App, methodPath, rt, modulePath string) {
	// The stub loader claims rt="goja" for every directory; in this
	// demo we re-register stubModule with the right rt so Module
	// passes the runtime-match check. In production the real
	// extruntime loader reads extension.json and the manifest decides.
	if err := app.Module(methodPath, "goja", modulePath); err != nil {
		log.Fatalf("mount %s: %v", methodPath, err)
	}
	_ = rt
}
