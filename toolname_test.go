package zip

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// #4: do MCP tool names ride the same rule as operation IDs?
//
// A tool name is what an agent CALLS and what MCP clients cache, so an
// ordering-dependent one is the same defect as an ordering-dependent operation
// ID. This checks the two halves separately, because they turn out to be
// different questions.
func TestToolNames_RideTheOperationIDRule(t *testing.T) {
	// Half one: a definition's OWN typed ops, included twice.
	billing := unnamedApp() // one op, id UNDECLARED — see unnamedApp
	host := quiet("host")
	host.Group("/v1").Use(billing)
	host.Group("/admin").Use(billing)
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	raw, names := host.composeTools()
	_ = raw
	var got []string
	for n := range names {
		got = append(got, n)
	}
	if len(got) != 2 {
		t.Fatalf("two occurrences produced %d tool names, want 2: %v", len(got), got)
	}
	for _, want := range []string{"v1.get_invoices_id", "admin.get_invoices_id"} {
		if !names[want] {
			t.Errorf("tool name %q missing — own-op tool names must be the occurrence's operation id: %v", want, got)
		}
	}

	// And the rendered list agrees with the registry, one name per operation.
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("tool list is not an array: %v", err)
	}
	if len(arr) != len(host.Registry()) {
		t.Errorf("tool list has %d entries, registry has %d", len(arr), len(host.Registry()))
	}
}

// Half two: a definition included twice must not produce two names for ONE
// callable thing. A plugin's catalogue is forwarded to the plugin's own MCP
// door by name — mcpForward never consults a prefix — so there is exactly one
// backend however many prefixes proxy to it.
func TestToolNames_OnePluginDefinitionIsOneTool(t *testing.T) {
	shared := quiet("shared")
	shared.plugMu.Lock()
	shared.pluginTools = []mcpTool{{name: "sharedTool", raw: json.RawMessage(`{"name":"sharedTool"}`)}}
	shared.plugMu.Unlock()

	host := quiet("host")
	host.Group("/v1").Use(shared)
	host.Group("/admin").Use(shared)
	// Give the host a route so the composition is buildable.
	host.Get("/health", func(c *Ctx) error { return nil })
	shared.Get("/x", func(c *Ctx) error { return nil })
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	n := 0
	for _, tl := range host.tools() {
		if tl.name == "sharedTool" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("one plugin definition at two prefixes produced %d tool entries, want 1 — "+
			"a catalogue tool is invoked by NAME at the plugin's own door, so two names would "+
			"invent a second callable that does not exist", n)
	}
}

func TestToolNames_AreNotPositional(t *testing.T) {
	namesFor := func(a, b string) []string {
		billing := unnamedApp()
		host := quiet("host")
		host.Group(a).Use(billing)
		host.Group(b).Use(billing)
		if err := host.Build(); err != nil {
			t.Fatalf("build: %v", err)
		}
		_, set := host.composeTools()
		var out []string
		for n := range set {
			out = append(out, n)
		}
		return out
	}
	x, y := namesFor("/v1", "/admin"), namesFor("/admin", "/v1")
	sortStrings(x)
	sortStrings(y)
	if strings.Join(x, ",") != strings.Join(y, ",") {
		t.Errorf("tool names depend on composition order: %v vs %v", x, y)
	}
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

var _ = context.Background
