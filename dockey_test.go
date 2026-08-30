package zip_test

// Documentation is filed in ONE map for a whole process, and an address is
// unique only within the app that serves it. Two chains in one node both answer
// GET /height, so before the key carried the declaring package, whichever init()
// ran last published its prose for the other's route — in the document and in
// the MCP tool alike. Nothing failed; only the sentence was wrong, which is why
// it survived every test until someone read the output.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zap-proto/zip"
)

type heightOut struct {
	Height uint64 `json:"height"`
}

func aHeight(_ context.Context, _ *struct{}) (*heightOut, error) { return &heightOut{}, nil }

// bHeight is a SECOND handler at the same address, standing in for the second
// chain. Both are declared in this package, so this test cannot show the
// package doing the separating — TestTwoAppsKeepTheirOwnProse does that with
// the real thing. What it shows is the key being read at all.
func bHeight(_ context.Context, _ *struct{}) (*heightOut, error) { return &heightOut{}, nil }

func descriptionOf(t *testing.T, app *zip.App, path string) string {
	t.Helper()
	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Description string `json:"description"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	return spec.Paths[path]["get"].Description
}

// TestProseIsFiledUnderItsPackage: two apps registering the same address each
// publish their OWN sentence, because the key names who declared it.
func TestProseIsFiledUnderItsPackage(t *testing.T) {
	const here = "github.com/zap-proto/zip_test"
	zip.Describe(zip.DocKey(here, "GET", "/height"), zip.Doc{Description: "The height this package means."})
	zip.Describe(zip.DocKey("some/other/chain", "GET", "/height"), zip.Doc{Description: "A different chain's height."})

	a := zip.New(zip.Config{AppName: "a", DisableStartupMessage: true})
	zip.Get(a, "/height", aHeight)
	if got := descriptionOf(t, a, "/height"); got != "The height this package means." {
		t.Errorf("got %q, want this package's own sentence", got)
	}

	b := zip.New(zip.Config{AppName: "b", DisableStartupMessage: true})
	zip.Get(b, "/height", bHeight)
	if got := descriptionOf(t, b, "/height"); got != "The height this package means." {
		t.Errorf("got %q — a handler declared here must read this package's key", got)
	}
}

// TestAnUnqualifiedKeyIsStillRead: a generated file written before the key
// carried a package is still correct for a process serving one app, which is
// most of them. It keeps working, and a qualified key wins where both exist.
func TestAnUnqualifiedKeyIsStillRead(t *testing.T) {
	zip.Describe("GET /legacy", zip.Doc{Description: "Filed the old way."})

	a := zip.New(zip.Config{AppName: "legacy", DisableStartupMessage: true})
	zip.Get(a, "/legacy", aHeight)
	if got := descriptionOf(t, a, "/legacy"); got != "Filed the old way." {
		t.Errorf("got %q, want the unqualified entry", got)
	}

	zip.Describe(zip.DocKey("github.com/zap-proto/zip_test", "GET", "/legacy"), zip.Doc{Description: "Filed under its package."})
	b := zip.New(zip.Config{AppName: "legacy2", DisableStartupMessage: true})
	zip.Get(b, "/legacy", aHeight)
	if got := descriptionOf(t, b, "/legacy"); got != "Filed under its package." {
		t.Errorf("got %q, want the qualified entry to win", got)
	}
}
