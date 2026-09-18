package zipdoc

import (
	"regexp"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

var described = regexp.MustCompile(`zip\.Describe\("([^"]+)"`)

// The key this generator writes for a RAW route has to be the key the registry
// finds. A raw route records no registrar — a method, a path and a handler
// chain — so an address is all anyone can ask by, while the generated file
// files it under the declaring package like every other entry. When the two
// spellings drifted, a service's whole untyped half documented as silent.
//
// twoaddr is two Raw routes with a doc comment, which is the shape that broke.
func TestRender_ARawRoutesKeyIsFoundByItsAddress(t *testing.T) {
	p := load(t, "twoaddr")

	src, err := p.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	keys := described.FindAllStringSubmatch(string(src), -1)
	if len(keys) != 2 {
		t.Fatalf("rendered %d Describe calls, want 2:\n%s", len(keys), src)
	}

	for _, m := range keys {
		key := m[1]
		f := strings.Fields(key)
		if len(f) != 3 {
			t.Fatalf("key %q is not <pkg> <method> <path>", key)
		}
		pkg, method, path := f[0], f[1], f[2]
		if pkg != p.Path {
			t.Errorf("key %q names package %q, want %q", key, pkg, p.Path)
		}

		zip.Describe(key, zip.Doc{Description: "The generated sentence for " + method + " " + path + "."})

		summary, _, ok := zip.Prose(method, path)
		if !ok {
			t.Errorf("%s: the generator wrote %q and the registry cannot find it by address", key, key)
			continue
		}
		if !strings.Contains(summary, path) {
			t.Errorf("%s: found some other entry: %q", key, summary)
		}
	}
}
