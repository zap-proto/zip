package zip_test

import (
	"context"
	"testing"

	"github.com/zap-proto/zip"
)

type wrapIn struct {
	ID string `json:"id"`
}

type wrapOut struct {
	OK bool `json:"ok"`
}

func wrapHandler(_ context.Context, _ *wrapIn) (*wrapOut, error) { return &wrapOut{OK: true}, nil }

// A doc comment WRAPS in Go source, and a sentence ends where the next one
// begins whether that is a space or a newline. The summary is neither of those
// facts: it is one sentence on one line, in the OpenAPI document, in the CLI and
// in the first docstring line of every generated SDK.
//
// Both shapes below shipped a raw line break into all three.
func TestSummary_IsOneSentenceOnOneLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{{
		name: "wrapped",
		// The first sentence runs past the column a doc comment wraps at, which
		// is the ordinary case, not an unusual one.
		doc:  "ListWorkers returns every worker script in the\naccount, newest first. Results are capped by limit.",
		want: "ListWorkers returns every worker script in the account, newest first.",
	}, {
		name: "sentence ends at the line break",
		// ". " never appears before the paragraph break, so the old rule kept
		// scanning and swallowed the second paragraph — line break and all.
		doc:  "Ping answers while the service is up.\n\nUse it as a health check. It touches no storage.",
		want: "Ping answers while the service is up.",
	}, {
		name: "no sentence at all",
		// No period anywhere: the summary is the first line, as it has always
		// been — just without the ragged edge.
		doc:  "Ping answers while the service is up\nand carries no body",
		want: "Ping answers while the service is up",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			app := zip.New(zip.Config{AppName: "wrap", DisableStartupMessage: true})
			zip.Describe("GET /v1/wrap/"+tc.name, zip.Doc{Description: tc.doc})
			zip.Get(app, "/v1/wrap/"+tc.name, wrapHandler)

			paths, _ := app.OpenAPISpec()["paths"].(map[string]map[string]any)
			op, _ := paths["/v1/wrap/"+tc.name]["get"].(map[string]any)
			if got, _ := op["summary"].(string); got != tc.want {
				t.Errorf("OpenAPI summary = %q, want %q", got, tc.want)
			}
			// The CLI reads the same rule from the same function. A summary is
			// one line there for a harder reason: it is printed in a list.
			cmds := app.Commands()
			if len(cmds) != 1 {
				t.Fatalf("commands = %d, want 1", len(cmds))
			}
			if cmds[0].Summary != tc.want {
				t.Errorf("CLI summary = %q, want %q", cmds[0].Summary, tc.want)
			}
		})
	}
}
