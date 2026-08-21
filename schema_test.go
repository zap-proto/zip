package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// A type that contains itself — a tree, which is an ordinary shape for an API to
// take. Before the schema registry it was fatal: the derivation walked the type
// forever, so BUILDING THE DOCUMENT overflowed the stack and took the process
// with it, and listing the MCP tools did the same. A service whose input has a
// self-reference could not start.
type treeNode struct {
	Name     string     `json:"name"`
	Children []treeNode `json:"children"`
}

// Mutual recursion is the same cycle with a longer way round, which is why it
// needs its own pin: a guard that only looked one level up would miss it.
type mutualA struct {
	B *mutualB `json:"b"`
}

type mutualB struct {
	A *mutualA `json:"a"`
}

type addr struct {
	City string `json:"city"`
}

// person names the SAME type twice. One type is one definition; two uses of it
// are two references to that one definition, not two copies of it.
type person struct {
	Name string `json:"name"`
	Home addr   `json:"home"`
	Work addr   `json:"work"`
}

// Op collides with zip.Op — two different types with one Go name, which is
// ordinary in an app composed from several packages. A registry that let the
// second overwrite the first would publish one type's fields under the other's
// name, and the $refs of both ops would point at whichever won.
type Op struct {
	Mine string `json:"mine"`
}

type collideIn struct {
	Mine  Op     `json:"mine"`
	Their zip.Op `json:"their"`
}

type schemaOut struct {
	OK bool `json:"ok"`
}

func ok[T any](_ context.Context, _ *T) (*schemaOut, error) { return &schemaOut{OK: true}, nil }

func schemaApp(t *testing.T) *zip.App {
	t.Helper()
	a := zip.New(zip.Config{AppName: "schema", DisableStartupMessage: true})
	zip.Post(a, "/v1/s/tree", ok[treeNode])
	zip.Post(a, "/v1/s/mutual", ok[mutualA])
	zip.Post(a, "/v1/s/person", ok[person])
	zip.Post(a, "/v1/s/collide", ok[collideIn])
	return a
}

// dig walks a nested map by key, failing the test at the first missing step so
// the message names WHERE the shape was wrong rather than "interface conversion".
func dig(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := m
	for i, k := range keys {
		next, ok := object(cur[k])
		if !ok {
			t.Fatalf("at %v: key %q is %#v, want an object", keys[:i], k, cur[k])
		}
		cur = next
	}
	return cur
}

// object reads either shape a spec node can have in Go: the document is built
// as plain maps, and "paths" happens to be typed one level tighter.
func object(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[string]map[string]any:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, true
	}
	return nil, false
}

func refOf(t *testing.T, m map[string]any) string {
	t.Helper()
	ref, ok := m["$ref"].(string)
	if !ok {
		t.Fatalf("schema %#v has no $ref", m)
	}
	return ref
}

// The whole point: a self-referential input yields a finite document. Reaching
// the assertions at all is most of the test — before the fix this function did
// not return, it aborted the test binary with "fatal error: stack overflow".
func TestSchema_SelfReferentialInputTerminates(t *testing.T) {
	spec := schemaApp(t).OpenAPISpec()

	schemas := dig(t, spec, "components", "schemas")
	if _, ok := schemas["treeNode"]; !ok {
		t.Fatalf("treeNode is not defined; schemas = %v", keysOf(schemas))
	}
	// The cycle is expressed the only way a schema can express one: the child
	// refers back to the definition it is inside.
	items := dig(t, schemas, "treeNode", "properties", "children", "items")
	if got, want := refOf(t, items), "#/components/schemas/treeNode"; got != want {
		t.Errorf("children.items = %q, want %q", got, want)
	}
	// And the operation points at that same one definition.
	body := dig(t, spec, "paths", "/v1/s/tree", "post", "requestBody", "content", "application/json", "schema")
	if got, want := refOf(t, body), "#/components/schemas/treeNode"; got != want {
		t.Errorf("requestBody schema = %q, want %q", got, want)
	}
}

// The tool list is derived from the same types, so it stops for the same reason.
// Its definitions travel in the schema's own $defs: an inputSchema is sent on its
// own and has to carry everything it refers to.
func TestSchema_MCPToolSchemaIsSelfContained(t *testing.T) {
	var tree map[string]any
	for _, tool := range schemaApp(t).MCPTools() {
		if tool["name"] == "post_s_tree" {
			tree, _ = tool["inputSchema"].(map[string]any)
		}
	}
	if tree == nil {
		t.Fatal("no tool for POST /v1/s/tree")
	}
	// The root is the object itself — a tool's inputSchema has to BE the
	// object, not a pointer at one.
	if tree["type"] != "object" {
		t.Fatalf("inputSchema = %#v, want an object at the root", tree)
	}
	items := dig(t, tree, "properties", "children", "items")
	if got, want := refOf(t, items), "#/$defs/treeNode"; got != want {
		t.Errorf("children.items = %q, want %q", got, want)
	}
	// …and what it refers to travels with it.
	if _, ok := dig(t, tree, "$defs")["treeNode"]; !ok {
		t.Errorf("$defs = %v, want treeNode carried alongside", keysOf(dig(t, tree, "$defs")))
	}
}

// Building a finite schema is only half of it: the tool list is MARSHALLED on
// every tools/list, and a schema that shared a map with its own $defs entry
// would encode forever. The route does this; so does this test.
func TestSchema_RecursiveToolListMarshals(t *testing.T) {
	done := make(chan []byte, 1)
	go func() {
		b, err := json.Marshal(schemaApp(t).MCPTools())
		if err != nil {
			t.Errorf("marshal tools: %v", err)
		}
		done <- b
	}()
	select {
	case b := <-done:
		if !bytes.Contains(b, []byte(`#/$defs/treeNode`)) {
			t.Errorf("marshalled tools do not carry the self-reference: %s", b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("marshalling the tool list did not finish — the schema graph is cyclic")
	}
}

// The document is served as bytes too, from one marshal at Prepare.
func TestSchema_RecursiveSpecMarshals(t *testing.T) {
	b, err := json.Marshal(schemaApp(t).OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if !bytes.Contains(b, []byte(`#/components/schemas/treeNode`)) {
		t.Errorf("marshalled spec does not carry the self-reference: %s", b)
	}
}

// A tool whose input reaches no other named type carries no $defs at all: the
// registry is what a schema NEEDS, not a wrapper every schema pays for.
func TestSchema_MCPToolSchemaCarriesNoUnusedDefs(t *testing.T) {
	a := zip.New(zip.Config{AppName: "schema", DisableStartupMessage: true})
	zip.Post(a, "/v1/s/flat", ok[addr])
	tools := a.MCPTools()
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want 1", tools)
	}
	s, _ := tools[0]["inputSchema"].(map[string]any)
	if _, ok := s["$defs"]; ok {
		t.Errorf("inputSchema = %#v, want no $defs — nothing refers to anything", s)
	}
	if _, ok := dig(t, s, "properties", "city")["type"]; !ok {
		t.Errorf("inputSchema = %#v, want the fields inlined", s)
	}
}

func TestSchema_MutuallyRecursiveTypesTerminate(t *testing.T) {
	schemas := dig(t, schemaApp(t).OpenAPISpec(), "components", "schemas")
	for _, name := range []string{"mutualA", "mutualB"} {
		if _, ok := schemas[name]; !ok {
			t.Fatalf("%s is not defined; schemas = %v", name, keysOf(schemas))
		}
	}
	if got, want := refOf(t, dig(t, schemas, "mutualA", "properties", "b")), "#/components/schemas/mutualB"; got != want {
		t.Errorf("mutualA.b = %q, want %q", got, want)
	}
	if got, want := refOf(t, dig(t, schemas, "mutualB", "properties", "a")), "#/components/schemas/mutualA"; got != want {
		t.Errorf("mutualB.a = %q, want %q", got, want)
	}
}

// One type, one definition. Two fields of the same type are two references to
// it — which is what a registry IS, and what makes a 40-op document readable
// instead of the same address inlined forty times.
func TestSchema_OneTypeIsDefinedOnceAndReferredTo(t *testing.T) {
	spec := schemaApp(t).OpenAPISpec()
	schemas := dig(t, spec, "components", "schemas")
	if _, ok := schemas["addr"]; !ok {
		t.Fatalf("addr is not defined; schemas = %v", keysOf(schemas))
	}
	props := dig(t, schemas, "person", "properties")
	home := refOf(t, dig(t, props, "home"))
	work := refOf(t, dig(t, props, "work"))
	if home != work || home != "#/components/schemas/addr" {
		t.Errorf("home = %q, work = %q, want both #/components/schemas/addr", home, work)
	}
}

// Two packages may both call a type Op. The document has one namespace, so the
// second one to arrive is qualified by its package rather than silently
// replacing the first — otherwise one op's document describes the other's type.
func TestSchema_SameNameFromTwoPackagesStaysTwoDefinitions(t *testing.T) {
	spec := schemaApp(t).OpenAPISpec()
	schemas := dig(t, spec, "components", "schemas")
	props := dig(t, schemas, "collideIn", "properties")
	mine, their := refOf(t, dig(t, props, "mine")), refOf(t, dig(t, props, "their"))
	if mine == their {
		t.Fatalf("both fields point at %q — one type overwrote the other", mine)
	}
	// The one that names a field the other does not is the one the field meant.
	if _, ok := dig(t, schemas, trimRef(mine), "properties")["mine"]; !ok {
		t.Errorf("%s does not describe the test's own Op", mine)
	}
	if _, ok := dig(t, schemas, trimRef(their), "properties")["OperationID"]; !ok {
		t.Errorf("%s does not describe zip.Op", their)
	}
}

func trimRef(ref string) string {
	const prefix = "#/components/schemas/"
	return ref[len(prefix):]
}
