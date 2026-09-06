// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zap-proto/zip/manifest"
)

type manIn struct {
	Chain  string `json:"chain" validate:"required"`
	Tenant string `json:"-" header:"X-Tenant"`
}

type manOut struct {
	Bootstrapped bool     `json:"isBootstrapped"`
	Peers        []string `json:"peers"`
}

func manApp(t *testing.T) *App {
	t.Helper()
	a := New(Config{AppName: "man", OpenAPI: OpenAPIConfig{Title: "man", Version: "v1"}})
	Get(a, "/chain/bootstrapped", func(_ context.Context, in *manIn) (*manOut, error) {
		return &manOut{}, nil
	})
	Post(a, "/chain/:chain/join", func(_ context.Context, in *manIn) (*manOut, error) {
		return &manOut{}, nil
	})
	return a
}

// A manifest is a VALUE: it survives the round trip through JSON, which is what
// lets the front end that writes it and the projector that reads it be two
// programs — or two languages.
func TestManifestRoundTripsAsJSON(t *testing.T) {
	m := manApp(t).Manifest()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back manifest.App
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, &back) {
		t.Fatal("the manifest did not survive the trip")
	}
	// And the projections of the copy are the projections of the original,
	// which is the property that actually matters.
	want, _ := json.Marshal(Document(m))
	got, _ := json.Marshal(Document(&back))
	if string(want) != string(got) {
		t.Fatalf("the document changed across the wire:\n%s\n%s", want, got)
	}
}

// The projector reads the manifest and nothing else: a manifest that never saw
// Go — one built by hand here, as a C++ or Rust pass would build it — projects
// to a document, a tool list, a command tree and a schema.
func TestAManifestWithNoGoBehindItProjects(t *testing.T) {
	m := &manifest.App{
		Name:    "info",
		Title:   "Lux node info",
		Version: "v1.36.178",
		Structs: map[string]manifest.Struct{
			"info.IsBootstrappedArgs": {
				Name: "IsBootstrappedArgs", Pkg: "info",
				Body: []manifest.Field{{Name: "Chain", JSON: "chain", URL: "chain", Required: true, Type: manifest.Type{Kind: manifest.String}}},
				Own:  []manifest.Field{{Name: "Chain", Type: manifest.Type{Kind: manifest.String}}},
			},
			"info.IsBootstrappedResponse": {
				Name: "IsBootstrappedResponse", Pkg: "info",
				Body: []manifest.Field{{Name: "IsBootstrapped", JSON: "isBootstrapped", URL: "isBootstrapped", Type: manifest.Type{Kind: manifest.Bool}}},
				Own:  []manifest.Field{{Name: "IsBootstrapped", Type: manifest.Type{Kind: manifest.Bool}}},
			},
		},
		Ops: []manifest.Op{{
			Method: "GET", Path: "/chain/bootstrapped", Pkg: "info",
			In:  &manifest.Type{Kind: manifest.Record, Name: "IsBootstrappedArgs", Pkg: "info", Ref: "info.IsBootstrappedArgs"},
			Out: &manifest.Type{Kind: manifest.Record, Name: "IsBootstrappedResponse", Pkg: "info", Ref: "info.IsBootstrappedResponse"},
			Doc: &manifest.Doc{
				Description: "Bootstrapped reports whether a chain has finished bootstrapping on this node.",
				Fields:      map[string]string{"IsBootstrappedArgs.chain": "Chain is the alias or id of the chain to ask about."},
				Example:     json.RawMessage(`{"chain":"X"}`),
			},
		}},
	}

	doc := Document(m)
	paths, _ := doc["paths"].(map[string]map[string]any)
	get, _ := paths["/chain/bootstrapped"]["get"].(map[string]any)
	if get["operationId"] != "get_chain_bootstrapped" {
		t.Errorf("operationId = %v", get["operationId"])
	}
	params, _ := get["parameters"].([]any)
	if len(params) != 1 {
		t.Fatalf("parameters = %v, want the chain query parameter", params)
	}
	p, _ := params[0].(map[string]any)
	if p["name"] != "chain" || p["in"] != "query" || p["required"] != true {
		t.Errorf("parameter = %v", p)
	}
	if p["description"] != "Chain is the alias or id of the chain to ask about." {
		t.Errorf("the field's own words did not reach the parameter: %v", p)
	}

	tools := Tools(m)
	if len(tools) != 1 || tools[0]["name"] != "get_chain_bootstrapped" {
		t.Fatalf("tools = %v", tools)
	}
	in, _ := tools[0]["inputSchema"].(map[string]any)
	props, _ := in["properties"].(map[string]any)
	if _, ok := props["chain"]; !ok {
		t.Errorf("the tool schema does not carry the argument: %v", in)
	}

	cmds := Commands(m)
	if len(cmds) != 1 || cmds[0].Service != "chain" {
		t.Fatalf("commands = %+v", cmds)
	}
	if len(cmds[0].Flags) != 1 || cmds[0].Flags[0].Name != "chain" {
		t.Errorf("flags = %+v", cmds[0].Flags)
	}

	s := ZAP("info", m)
	if s.Ops() != 1 {
		t.Fatalf("schema carries %d ops:\n%s", s.Ops(), s)
	}
	if len(s.Structs) != 2 || s.Structs[0].Fields[0].Type != "text" {
		t.Fatalf("schema:\n%s", s)
	}
}
