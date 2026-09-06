// SPDX-License-Identifier: BSD-3-Clause-Eco

package zip

// The gate on the C++ front end.
//
// cpp/zipcpp reads a C++ service with Clang and writes the .zap schema it
// declares; everything downstream — the OpenAPI document, the MCP tool list, the
// CLI — is computed from that file by the same back end a Go service's schema
// goes through. Two front ends therefore state the wire rule twice: once in
// internal/zapenc.LayoutOf, which the encoder speaks, and once in
// cpp/zipcpp/schema.hpp, which the emitter writes. This is what keeps them one
// rule.
//
// The first assertion is the sharp one, and it is not written here: [ReadZAP]
// derives the layout again from the file and REFUSES any field whose stated
// offset differs from the derived one, naming every disagreement. So a C++
// emitter that drifted by a single byte fails this test at the read, before
// anything is compared.
//
// What is compared here is the rest of the claim: that the same service declared
// in C++ and in Go produces the same structs, in the same order, with the same
// wire types at the same offsets, under the same names on the JSON edge.

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/zap-proto/go/idl"
	"github.com/zap-proto/zip/internal/jsontag"
)

const cppSchema = "cpp/example/accounts.zap"

// The Go declaration of the service cpp/example/accounts.hpp declares. It exists
// only here: it is the other front end's statement of the same shape, and the
// test is that the two agree.
type cppStanding uint8

type (
	cppAccount struct {
		ID       string      `json:"id"`
		Cents    int64       `json:"cents"`
		Currency string      `json:"currency"`
		State    cppStanding `json:"state"`
	}
	cppLookup struct {
		ID string `json:"id"`
	}
	cppEntry struct {
		Account   string `json:"account"`
		Cents     int64  `json:"cents"`
		Memo      string `json:"memo"`
		Reference []byte `json:"reference"`
	}
	cppReceipt struct {
		Seq   uint64     `json:"seq"`
		Moved cppAccount `json:"moved"`
		Seal  [32]byte   `json:"seal"`
	}
	cppWindow struct {
		Offset     uint32 `json:"offset"`
		Limit      uint16 `json:"limit"`
		Descending bool   `json:"descending"`
	}
	cppPage struct {
		Accounts []cppAccount `json:"accounts"`
		Total    uint32       `json:"total"`
	}
)

// The alignment torture, in Go. cpp/zipcpp/testdata/shapes.hpp declares the same
// members in the same order.
type cppInner struct {
	N int32 `json:"n"`
}

type cppShapes struct {
	Flag     bool       `json:"flag"`
	Wide     uint64     `json:"wide"`
	Small    uint8      `json:"small"`
	Ratio    float32    `json:"ratio"`
	Narrow   int16      `json:"narrow"`
	Tag      [3]byte    `json:"tag"`
	Exact    float64    `json:"exact"`
	Counts   []int16    `json:"counts"`
	Label    string     `json:"label"`
	Blob     []byte     `json:"blob"`
	One      cppInner   `json:"one"`
	Many     []cppInner `json:"many"`
	Trailing int8       `json:"trailing"`
}

func readCppSchema(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile(cppSchema)
	if err != nil {
		t.Fatalf("the C++ front end's output is the input to every projection: %v", err)
	}
	return src
}

// TestCppSchemaLaysOutAsGoDoes is the agreement between the two front ends.
func TestCppSchemaLaysOutAsGoDoes(t *testing.T) {
	src := readCppSchema(t)

	// The offset gate. ReadZAP derives every layout again and refuses a field
	// whose stated offset is not the derived one.
	if _, err := ReadZAP(cppSchema, src); err != nil {
		t.Fatalf("the C++ emitter and this encoder describe different wires: %v", err)
	}

	file, err := idl.Parse(cppSchema, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	compare(t, file, map[string]reflect.Type{
		"account": reflect.TypeFor[cppAccount](),
		"entry":   reflect.TypeFor[cppEntry](),
		"lookup":  reflect.TypeFor[cppLookup](),
		"page":    reflect.TypeFor[cppPage](),
		"receipt": reflect.TypeFor[cppReceipt](),
		"window":  reflect.TypeFor[cppWindow](),
	})
}

// compare asserts that each struct the schema declares has the fields the Go
// declaration of the same shape lays out: the same names on the JSON edge, the
// same wire types, at the same offsets, in the same order.
func compare(t *testing.T, file *idl.File, want map[string]reflect.Type) {
	t.Helper()
	if len(file.Structs) != len(want) {
		t.Fatalf("the schema declares %d structs, the Go declaration %d", len(file.Structs), len(want))
	}
	for _, s := range file.Structs {
		got, ok := want[s.Name]
		if !ok {
			t.Errorf("struct %s is in the schema and not in the Go declaration", s.Name)
			continue
		}
		shape, err := LayoutOf(got)
		if err != nil {
			t.Errorf("%s: %v", s.Name, err)
			continue
		}
		if len(shape.Slots) != len(s.Fields) {
			t.Errorf("%s: %d fields in the schema, %d slots in the layout",
				s.Name, len(s.Fields), len(shape.Slots))
			continue
		}
		for i, slot := range shape.Slots {
			f := s.Fields[i]
			sf := got.Field(i)
			name := jsontag.Name(sf.Name, sf.Tag.Get("json"))
			if f.Name != name {
				t.Errorf("%s field %d: the schema calls it %s, the Go declaration %s",
					s.Name, i, f.Name, name)
			}
			if f.Offset != slot.Offset {
				t.Errorf("%s.%s: the schema states @%d, the layout derives @%d",
					s.Name, f.Name, f.Offset, slot.Offset)
			}
			if say := zapType(f.Type); say != slot.Type {
				t.Errorf("%s.%s: the schema states %s, the layout derives %s",
					s.Name, f.Name, say, slot.Type)
			}
		}
	}
}

// TestCppShapesLayOutAsGoDoes is the same comparison over the shape the two
// statements of the layout rule are most likely to disagree about: a one-byte
// value between two eight-byte ones, and a fixed run, which aligns to 1 where
// everything else aligns to its own width.
func TestCppShapesLayOutAsGoDoes(t *testing.T) {
	const path = "cpp/zipcpp/testdata/shapes.zap"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadZAP(path, src); err != nil {
		t.Fatalf("the C++ emitter and this encoder describe different wires: %v", err)
	}
	file, err := idl.Parse(path, src)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, file, map[string]reflect.Type{
		"shapes": reflect.TypeFor[cppShapes](),
		"done": reflect.TypeFor[struct {
			OK bool `json:"ok"`
		}](),
	})
}

// zapType is how one parsed field type reads, in the words [LayoutOf] uses for
// the same thing.
func zapType(t idl.Type) string {
	switch t.Kind {
	case idl.KindBytesFixed:
		return "bytes_fixed[" + strconv.Itoa(t.FixedSize) + "]"
	case idl.KindList:
		return "list<" + zapType(*t.ListElem) + ">"
	default:
		return t.Kind.String()
	}
}

// TestCppSchemaProjects is the whole point: the file the C++ front end wrote is
// the registry every projection reduces over.
func TestCppSchemaProjects(t *testing.T) {
	apps, err := ReadZAP(cppSchema, readCppSchema(t))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("the schema declares %d interfaces, want 1", len(apps))
	}
	app := apps[0]
	if app.Name() != "accounts" {
		t.Errorf("interface is named %q", app.Name())
	}

	ops := []string{"list_", "post", "read"}

	doc, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if !strings.Contains(string(doc), `"operationId":"`+op+`"`) {
			t.Errorf("the OpenAPI document does not name %s", op)
		}
	}

	tools := app.MCPTools()
	if len(tools) != len(ops) {
		t.Fatalf("the MCP tool list has %d tools, want %d", len(tools), len(ops))
	}
	for i, tool := range tools {
		if tool["name"] != ops[i] {
			t.Errorf("MCP tool %d is %v, want %s", i, tool["name"], ops[i])
		}
	}

	cmds := app.Commands()
	if len(cmds) != len(ops) {
		t.Fatalf("the CLI has %d commands, want %d", len(cmds), len(ops))
	}
	for i, c := range cmds {
		if c.OperationID != ops[i] {
			t.Errorf("CLI command %d is %s, want %s", i, c.OperationID, ops[i])
		}
	}
}

// TestCppSchemaCarriesNoProse records what the pivot loses today, so that the
// day it stops losing it is a deliberate change to this test and not a surprise.
//
// The C++ source documents every operation and every member with `///`, and
// zipcpp writes each sentence into the schema above the declaration it is about.
// idl.Parse skips a '#' comment as whitespace, so no node carries it: the
// registry [ReadZAP] builds has no prose, and every projection of it is silent.
// An MCP tool with an empty description is the sharp end of that — an agent
// chooses a tool by reading one.
func TestCppSchemaCarriesNoProse(t *testing.T) {
	src := readCppSchema(t)
	if !strings.Contains(string(src), "# Read one account by id.") {
		t.Fatal("the schema no longer carries the C++ prose; the emitter regressed")
	}

	apps, err := ReadZAP(cppSchema, src)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, tool := range apps[0].MCPTools() {
		if tool["description"] != "" {
			t.Fatalf("prose now crosses the schema: %v — close this gap in the test too", tool)
		}
	}
}

// TestCppRefusalsCostOnlyTheirOwnOps holds the shape of a refusal.
//
// cpp/zipcpp/testdata/refused.hpp declares eight operations that cannot cross —
// an optional, a map, a pointer, a list of lists, a fixed run of integers, a
// struct that contains itself, a struct with a base class, and a method taking
// two payloads — beside two that can. Each refusal costs its own operation and
// nothing else: the service is still a service, and the ledger in the file names
// every one of the eight.
func TestCppRefusalsCostOnlyTheirOwnOps(t *testing.T) {
	const path = "cpp/zipcpp/testdata/refused.zap"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := ReadZAP(path, src)
	if err != nil {
		t.Fatalf("what survived the refusals must still be readable: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("%d interfaces, want 1", len(apps))
	}
	var ops []string
	for _, op := range apps[0].Registry() {
		ops = append(ops, op.OperationID)
	}
	if !reflect.DeepEqual(ops, []string{"bare", "sink"}) {
		t.Errorf("ops are %v, want the two that cross", ops)
	}

	// The eight are named in the file, so a reader learns the cost without
	// counting what is missing.
	if !strings.Contains(string(src), "# blocked (8)") {
		t.Error("the ledger does not name eight blocked operations")
	}
	for _, why := range []string{
		"may be absent, and the schema has no way to say so",
		"a list of lists has no wire form",
		"contains itself, so it has no fixed width",
		"a base class has no wire form",
		"a method carries at most one struct in each direction",
		"an array of int has no wire form",
	} {
		if !strings.Contains(string(src), why) {
			t.Errorf("the ledger does not say %q", why)
		}
	}
}

// TestCppProjectionsAreCurrent holds the three files cpp/example carries beside
// the schema.
//
// They are committed because a projection nobody can read is a claim, and they
// are checked here because a committed projection that stopped matching its
// source is worse than none. The command in the failure is the one that wrote
// each of them; there is no second way to regenerate them.
func TestCppProjectionsAreCurrent(t *testing.T) {
	apps, err := ReadZAP(cppSchema, readCppSchema(t))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	app := apps[0]

	doc, err := json.MarshalIndent(app.OpenAPISpec(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cli, err := json.MarshalIndent(app.Commands(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mcp, err := app.CppMCP("accounts")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []struct {
		path string
		have []byte
		how  string
	}{
		{"cpp/example/openapi.json", append(doc, '\n'),
			"zipgen openapi -schema cpp/example/accounts.zap -o cpp/example/openapi.json"},
		{"cpp/example/cli.json", cli,
			"zipgen cli -schema cpp/example/accounts.zap -lang go -o cpp/example/cli.json"},
		{"cpp/example/mcp.hpp", mcp.Header,
			"zipgen mcp -schema cpp/example/accounts.zap -lang cpp -pkg accounts -o cpp/example/mcp.hpp"},
	} {
		on, err := os.ReadFile(want.path)
		if err != nil {
			t.Errorf("%v — write it with: %s", err, want.how)
			continue
		}
		if string(on) != string(want.have) {
			t.Errorf("%s is not what the schema projects; rewrite it with: %s", want.path, want.how)
		}
	}
}
