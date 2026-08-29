package zip

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type hiddenIn struct{}
type hiddenOut struct {
	OK bool `json:"ok"`
}

// Undeclared promises a route serves without appearing "in none of the
// projections built from [App.Declaration]: the OpenAPI document, the MCP tool
// list, the CLI commands, the by-name call plane."
//
// The MCP half was not held: composeTools reads Registry(), which is every
// registered op, while the undeclared fact rides the route entry. So an op
// deliberately kept out of the contract was still offered to an agent — the
// one projection where being callable matters most.
func TestAnUndeclaredTypedOpIsNotAnMCPTool(t *testing.T) {
	app := New(Config{AppName: "hide"})
	Get(app, "/seen", func(ctx context.Context, in *hiddenIn) (*hiddenOut, error) {
		return &hiddenOut{OK: true}, nil
	})
	Get(Undeclared(app), "/unseen", func(ctx context.Context, in *hiddenIn) (*hiddenOut, error) {
		return &hiddenOut{OK: true}, nil
	})

	tools, _ := app.composeTools()
	var got []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(tools, &got); err != nil {
		t.Fatalf("tools: %v", err)
	}
	var names []string
	for _, x := range got {
		names = append(names, x.Name)
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "get_seen") {
		t.Errorf("the declared op is missing from the tool list: %v", names)
	}
	if strings.Contains(joined, "get_unseen") {
		t.Errorf("an UNDECLARED op was offered as an MCP tool: %v", names)
	}
}

// The route still SERVES — hiding it from the contract must not unmount it.
func TestAnUndeclaredOpStillAnswers(t *testing.T) {
	app := New(Config{AppName: "hide"})
	Get(Undeclared(app), "/unseen", func(ctx context.Context, in *hiddenIn) (*hiddenOut, error) {
		return &hiddenOut{OK: true}, nil
	})
	if app.Declares("GET", "/unseen") {
		t.Error("an undeclared route appeared in the declaration")
	}
	if len(app.Registry()) != 1 {
		t.Errorf("the op left the registry entirely: %d", len(app.Registry()))
	}
}
