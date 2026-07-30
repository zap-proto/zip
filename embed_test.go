package zip_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// patch is a body type shared by two routes, embedded rather than repeated. It is
// UNEXPORTED, which is the ordinary shape for one: it is this package's internal
// detail, not part of anyone's API.
type patch struct {
	Enabled *bool     `json:"enabled,omitempty"`
	State   *string   `json:"state,omitempty"`
	Orgs    *[]string `json:"orgs,omitempty"`
}

type embedIn struct {
	// Name is the thing being patched, from the URL.
	Name string `json:"name"`
	patch
}

type embedOut struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func embedHandler(_ context.Context, in *embedIn) (*embedOut, error) {
	out := &embedOut{Name: in.Name}
	if in.State != nil {
		out.State = *in.State
	}
	return out, nil
}

// PatchThing sets one thing's state.
func patchThing(ctx context.Context, in *embedIn) (*embedOut, error) { return embedHandler(ctx, in) }

// TestEmbeddedBodyFieldsReachEveryProjection pins what encoding/json already does
// and every projection used not to: an embedded struct's fields are PROMOTED onto
// the outer object, so they are part of the request.
//
// Four things asked "what fields does this type carry" with their own loop over
// NumField, and an embedded type is very often unexported, so all four skipped it
// on IsExported and dropped every field it promoted. The wire took them the whole
// time — the decoder never had the bug — which is why nothing failed:
//
//   - the schema published an object with only the outer fields;
//   - the document declared NO request body at all when the outer fields were all
//     path params, because "every field is a path param" was true of the fields it
//     could see;
//   - the CLI offered no flag for them, so the command could not send a body;
//   - bindURL could not bind one from the URL.
//
// The shape below is the real one that surfaced it: cloud's PATCH
// /v1/admin/catalog/providers/{name}, whose input is a path param plus an embedded
// unexported patch body. Under the old rule it published no body, and every
// generated SDK would have had no way to send the patch.
func TestEmbeddedBodyFieldsReachEveryProjection(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	zip.Patch(app, "/v1/things/:name", patchThing)

	spec, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody *struct {
				Content map[string]struct {
					Schema json.RawMessage `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("spec: %v", err)
	}

	// (a) The document declares a request body. It once declared none: `name` is a
	// path param, and it was the only field the old rule could see.
	op, ok := doc.Paths["/v1/things/{name}"]["patch"]
	if !ok {
		t.Fatal("PATCH /v1/things/{name} is not in the document")
	}
	if op.RequestBody == nil {
		t.Fatal("no requestBody — the promoted patch fields are the body, and a client " +
			"reading this document has no way to send them")
	}

	// (b) The schema names the promoted fields.
	want := []string{"enabled", "state", "orgs", "name"}
	body := string(op.RequestBody.Content["application/json"].Schema)
	props := map[string]json.RawMessage{}
	if strings.Contains(body, "$ref") {
		name := body[strings.LastIndex(body, "/")+1:]
		name = strings.Trim(name, `"}`)
		props = doc.Components.Schemas[name].Properties
	} else {
		var s struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		_ = json.Unmarshal([]byte(body), &s)
		props = s.Properties
	}
	for _, f := range want {
		if _, ok := props[f]; !ok {
			t.Errorf("schema has no %q — promoted fields are part of the object", f)
		}
	}

	// (c) The MCP tool's inputSchema is the same set, because it is the same rule.
	tools := app.MCPTools()
	if len(tools) != 1 {
		t.Fatalf("MCP tools = %d, want 1", len(tools))
	}
	in, _ := json.Marshal(tools[0]["inputSchema"])
	for _, f := range want {
		if !strings.Contains(string(in), `"`+f+`"`) {
			t.Errorf("MCP inputSchema has no %q: %s", f, in)
		}
	}

	// (d) The CLI carries a flag for each promoted field, or the command cannot
	// send the body at all.
	cmds := app.Commands()
	if len(cmds) != 1 {
		t.Fatalf("commands = %d, want 1", len(cmds))
	}
	flags := map[string]bool{}
	for _, f := range cmds[0].Flags {
		flags[f.Name] = true
	}
	for _, f := range []string{"enabled", "state", "orgs"} {
		if !flags[f] {
			t.Errorf("no --%s flag; flags = %v", f, flags)
		}
	}

	// (e) And the wire still does what it always did: the body's promoted fields
	// decode, and the path still names the target.
	code, body2 := call2(t, app, "PATCH", "/v1/things/widget", `{"state":"beta"}`)
	if code != 200 {
		t.Fatalf("PATCH = %d %s, want 200", code, body2)
	}
	for _, want := range []string{`"name":"widget"`, `"state":"beta"`} {
		if !strings.Contains(body2, want) {
			t.Errorf("body %s is missing %s", body2, want)
		}
	}
}
