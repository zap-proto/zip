package zip_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/runtime"
)

// stubLoader is a minimal runtime.Loader fixture for tests.
type stubLoader struct{ rt string }

func (s stubLoader) Runtimes() []string { return []string{s.rt} }
func (s stubLoader) LoadDir(_ context.Context, _ string) (map[string]runtime.Module, error) {
	return map[string]runtime.Module{}, nil
}
func (s stubLoader) LoadOne(_ context.Context, dir string) (runtime.Module, error) {
	return &stubModule{dir: dir, rt: s.rt}, nil
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
		"status":  201,
		"headers": map[string]string{"X-Test": "1"},
		"body":    map[string]any{"fn": fn, "echo": json.RawMessage(payload)},
	})
}

func TestModuleRoute(t *testing.T) {
	app := zip.New(zip.Config{
		DisableStartupMessage: true,
		Loader:                stubLoader{rt: "goja"},
		AllowedRuntimes:       []string{"goja"},
	})
	if err := app.Module("POST /v1/eval", "goja", "./fixtures/eval"); err != nil {
		t.Fatalf("Module: %v", err)
	}
	req, _ := http.NewRequest("POST", "/v1/eval", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != 201 {
		t.Fatalf("status %d, want 201", resp.StatusCode)
	}
	if resp.Header.Get("X-Test") != "1" {
		t.Fatalf("X-Test header missing")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"fn":"eval"`) {
		t.Fatalf("body=%s", body)
	}
}

func TestModuleRouteRuntimeMismatch(t *testing.T) {
	app := zip.New(zip.Config{
		DisableStartupMessage: true,
		Loader:                stubLoader{rt: "goja"},
	})
	if err := app.Module("POST /v1/eval", "wazero", "./fixtures/eval"); err == nil {
		t.Fatal("expected error for runtime mismatch")
	}
}

func TestModuleRouteAllowedRuntimes(t *testing.T) {
	app := zip.New(zip.Config{
		DisableStartupMessage: true,
		Loader:                stubLoader{rt: "pyvm"},
		AllowedRuntimes:       []string{"goja", "wazero"}, // pyvm not allowed
	})
	if err := app.Module("POST /v1/eval", "pyvm", "./fixtures/eval"); err == nil {
		t.Fatal("expected error for disallowed runtime")
	}
}

func TestModuleRouteNoLoader(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := app.Module("POST /v1/eval", "goja", "./fixtures/eval"); err == nil {
		t.Fatal("expected error when Loader is nil")
	}
}
