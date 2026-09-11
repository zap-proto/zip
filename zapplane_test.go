package zip_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The op-call plane is ZAP and only ZAP. JSON belongs to the adaptors that face
// something which speaks it — a browser on REST, an agent on MCP — and a JSON
// import in a file that builds or reads the plane's messages is the plane
// quietly growing a second wire. This holds the line where it is drawn.
func TestTheZAPPlaneSpeaksNoJSON(t *testing.T) {
	plane, err := filepath.Glob(filepath.Join("internal", "zapwire", "*.go"))
	if err != nil || len(plane) == 0 {
		t.Fatalf("internal/zapwire: %v", err)
	}
	plane = append(plane, "call.go", "wire.go", "layout.go")
	for _, path := range plane {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			switch p, _ := strconv.Unquote(imp.Path.Value); p {
			case "encoding/json", "encoding/json/v2", "github.com/zap-proto/zip/internal/jsonenc":
				t.Errorf("%s imports %s; the ZAP plane carries ZAP only", path, p)
			}
		}
		// zip.go routes fiber's JSON through these two, so they reach JSON
		// without an import.
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range []string{"jsonMarshal(", "jsonUnmarshal("} {
			if strings.Contains(string(src), fn) {
				t.Errorf("%s calls %s; the ZAP plane carries ZAP only", path, fn)
			}
		}
	}
}
