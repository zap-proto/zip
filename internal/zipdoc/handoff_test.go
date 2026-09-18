package zipdoc

import (
	"path/filepath"
	"strings"
	"testing"
)

// A group handed to another function has a prefix this pass cannot see, and
// the op's own path is only the part after it. Filing the comment at that
// partial path puts it under an address nothing serves — which is the silent
// drop this resolution exists to end — so it is refused and the call is named.
func TestExtract_AGroupHandedAcrossAFunctionIsRefused(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "handoff"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, nil); err == nil {
		t.Fatal("a group reached through a parameter resolved to the empty prefix, so /v1/thing/replay documented as /replay")
	} else if !strings.Contains(err.Error(), "cannot resolve the path prefix") {
		t.Fatalf("refused for some other reason: %v", err)
	}
}
