package jsonenc

import (
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"io"
)

// Variant names the JSON implementation. zip.New() logs it once at startup.
const Variant = "encoding/json/v2"

// wire is what zip's JSON says, stated once and passed to every call.
//
// It starts from v1's semantics — which is the wire every client already
// reads — and adds the one v2 behaviour worth having everywhere: a map's keys
// come out sorted, so a document built twice is the same bytes. That matters
// wherever bytes are compared or stored: an ETag, a cache key, a signature, a
// golden file, a git blob.
//
// The rest of v2's semantics are NOT taken silently. Each one moves the wire:
// omitempty stops omitting false and 0, a nil slice becomes [] and a nil map
// {}, `<` stops being escaped, and a field name that differs only in case stops
// matching. Any of them may be worth having, and each is a decision to make
// once, in this one place, with the clients in view — not something to inherit
// because a toolchain flipped a default.
var wire = json.JoinOptions(
	jsonv1.DefaultOptionsV1(),
	json.Deterministic(true),
)

// Marshal encodes v as JSON.
func Marshal(v any) ([]byte, error) { return json.Marshal(v, wire) }

// Unmarshal decodes data into v.
func Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v, wire) }

// Write encodes v straight to w. A response written this way never exists as a
// second copy in memory, which is the whole reason to hold the encoder rather
// than a []byte.
func Write(w io.Writer, v any) error { return json.MarshalWrite(w, v, wire) }

// Read decodes the whole of r into v, so a request body is never buffered
// twice.
func Read(r io.Reader, v any) error { return json.UnmarshalRead(r, v, wire) }
