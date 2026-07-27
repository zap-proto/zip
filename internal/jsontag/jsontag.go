// Package jsontag resolves a struct field's name on the wire.
//
// It exists so that rule lives in ONE place. zip reads it three ways — from
// reflect when building a schema, from reflect when binding path params, and
// from the AST when cmd/zipdoc extracts field documentation — and a spec that
// names a field differently from the decoder that reads it is precisely the
// drift the generator exists to kill. Different inputs, same rule.
package jsontag

import "strings"

// Name is the JSON name of a field declared as goName with the given `json`
// struct tag value (the tag's value, not the whole tag literal).
//
// The tag's name wins; an empty or options-only tag (`json:",omitempty"`)
// leaves the Go name. "-" is returned as-is rather than resolved, so a caller
// can tell "this field is omitted" from "this field is named -".
func Name(goName, tag string) string {
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return goName
	}
	return tag
}
