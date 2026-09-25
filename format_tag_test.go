package zip

import (
	"reflect"
	"testing"
)

// A secret is a string that says so. The tag reaches the document as the
// schema's format, which is what a generated client reads to keep the value
// off the command line.
func TestAFormatTagDescribesTheString(t *testing.T) {
	type put struct {
		Name  string `json:"name"`
		Value string `json:"value" format:"password"`
		Count int    `json:"count" format:"password"`
	}
	props, _ := rootSchemaOf(reflect.TypeOf(put{}), nil)["properties"].(map[string]any)
	value, _ := props["value"].(map[string]any)
	if value["type"] != "string" || value["format"] != "password" {
		t.Fatalf("value: got %v, want a string with format password", value)
	}
	name, _ := props["name"].(map[string]any)
	if _, has := name["format"]; has {
		t.Fatalf("name carries a format it never declared: %v", name)
	}
	// Only a string has a string format; an integer keeps its own.
	count, _ := props["count"].(map[string]any)
	if count["format"] == "password" {
		t.Fatalf("count took a string format: %v", count)
	}
}
