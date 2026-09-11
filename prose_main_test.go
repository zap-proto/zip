package zip_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProseReachesAServiceInPackageMain runs cmd/zipdoc over a service whose
// handler is in package main, then asks the built program for its OpenAPI
// document. A test binary names package main by its import path, so only a real
// program shows whether the prose zipdoc filed is found at run time.
func TestProseReachesAServiceInPackageMain(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a program with the go toolchain")
	}
	zipDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	directive := ""
	for _, line := range strings.Split(string(mod), "\n") {
		if strings.HasPrefix(line, "go ") {
			directive = line
			break
		}
	}

	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/prose\n\n"+directive+
		"\n\nrequire github.com/zap-proto/zip v1.36.47\n\nreplace github.com/zap-proto/zip => "+zipDir+"\n")
	write("go.sum", string(sum))
	write("main.go", proseMain)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			if strings.Contains(string(out), "missing go.sum entry") || strings.Contains(string(out), "cannot find module") {
				t.Skipf("module cache does not hold zip's dependencies: %s", out)
			}
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("run", "github.com/zap-proto/zip/cmd/zipdoc")
	run("run", ".", "openapi", "openapi.json")

	raw, err := os.ReadFile(filepath.Join(dir, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Description string `json:"description"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Paths["/v1/hello"]["get"].Description; got != "Greets whoever asks." {
		t.Errorf("description = %q, want the doc comment zipdoc read", got)
	}
}

const proseMain = `package main

import (
	"context"
	"os"

	"github.com/zap-proto/zip"
)

// Greeting is what Hello answers.
type Greeting struct {
	Message string ` + "`json:\"message\"`" + `
}

// Empty takes nothing.
type Empty struct{}

// Hello greets whoever asks.
func Hello(context.Context, *Empty) (*Greeting, error) {
	return &Greeting{Message: "hello"}, nil
}

func main() {
	app := zip.New(zip.Config{AppName: "prose", DisableStartupMessage: true})
	zip.Get(app, "/v1/hello", Hello)
	if done, err := app.Described(); done && err != nil {
		os.Exit(1)
	}
}
`
