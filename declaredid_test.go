package zip

import (
	"context"
	"strings"
	"testing"
)

// A DECLARED operation id survives composition, verbatim.
//
// This is the o11y shape, minimised: a service declares its ops with the names
// it publishes, and a host includes it under a prefix. Before this rule the
// prefix was folded into every id — `CreateRole` became `v1.o11y.CreateRole` —
// so composing the service renamed all 353 of its published operations, and
// with them every MCP tool an agent had cached, every generated SDK method,
// every CLI command and every operationId in the document. A wiring line is not
// allowed to do that.
//
// The assertion is deliberately on ALL FIVE projections that read the id, not
// just the document, because the defect was in the one place they all read.

type roleIn struct {
	Name string `json:"name"`
}

type roleOut struct {
	ID string `json:"id"`
}

// service is the o11y shape: a definition that declares its own published ids.
func service() *App {
	s := quiet("o11y")
	Post(s, "/roles", func(_ context.Context, in *roleIn) (*roleOut, error) {
		return &roleOut{ID: in.Name}, nil
	}, WithOperationID("CreateRole"))
	return s
}

func TestDeclaredOperationIDSurvivesComposition(t *testing.T) {
	host := quiet("host")
	host.Group("/v1/o11y").Use(service())
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	reg := host.Registry()
	if len(reg) != 1 {
		t.Fatalf("registry has %d ops, want 1", len(reg))
	}
	if got := reg[0].OperationID; got != "CreateRole" {
		t.Errorf("registry: operation id = %q, want %q — an explicitly declared id must "+
			"survive composition verbatim, not be qualified by the host's prefix", got, "CreateRole")
	}
	// The path IS composed — only the NAME is the author's.
	if got := reg[0].Path; got != "/v1/o11y/roles" {
		t.Errorf("path = %q, want /v1/o11y/roles — composition still owns the address", got)
	}

	// The document.
	doc := host.OpenAPISpec()
	paths, _ := doc["paths"].(map[string]map[string]any)
	post, _ := paths["/v1/o11y/roles"]["post"].(map[string]any)
	if post == nil {
		t.Fatalf("no post operation at /v1/o11y/roles: %v", paths)
	}
	if got := post["operationId"]; got != "CreateRole" {
		t.Errorf("document: operationId = %v, want CreateRole", got)
	}

	// The MCP tool name.
	_, tools := host.composeTools()
	if !tools["CreateRole"] {
		var got []string
		for n := range tools {
			got = append(got, n)
		}
		t.Errorf("MCP tool names = %v, want CreateRole", got)
	}

	// The by-name call plane.
	if op := host.opByName("CreateRole"); op == nil {
		t.Errorf("CreateRole is not resolvable by name")
	}
}

// The other half of the rule, and the reason the derivation exists at all: an
// op that declares NO id takes the one its ABSOLUTE path spells, so one
// definition included twice yields two distinguishable ids and neither is a
// function of mount order.
func TestUndeclaredOperationIDComesFromTheAbsolutePath(t *testing.T) {
	anon := quiet("anon")
	Get(anon, "/invoices/:id", func(_ context.Context, in *invoiceIn) (*invoiceOut, error) {
		return &invoiceOut{ID: in.ID}, nil
	})

	host := quiet("host")
	host.Group("/v1").Use(anon)
	host.Group("/admin").Use(anon)
	if err := host.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	got := map[string]bool{}
	for _, op := range host.Registry() {
		got[op.OperationID] = true
	}
	for _, want := range []string{"get_invoices_by_id", "get_admin_invoices_by_id"} {
		if !got[want] {
			t.Errorf("id %q missing from %v — an UNDECLARED id must be derived from the "+
				"absolute path its occurrence answers at", want, got)
		}
	}
}

// Where the two rules meet: a DECLARED id and two occurrences is a refused
// program, not a silent rename. The refusal is what makes the survival rule
// safe — the ambiguity is reported to the author rather than resolved behind
// their back by editing their published name.
func TestDeclaredOperationIDIncludedTwiceIsRefused(t *testing.T) {
	svc := service() // ONE definition, two inclusion sites
	host := quiet("host")
	host.Group("/v1").Use(svc)
	host.Group("/admin").Use(svc)

	err := host.Build()
	if err == nil {
		t.Fatalf("two occurrences of a declared id must be refused")
	}
	msg := err.Error()
	for _, want := range []string{"CreateRole", "declared once and occurs twice", "WithOperationID"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q: %s", want, msg)
		}
	}
}
