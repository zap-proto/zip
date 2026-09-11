//go:build goexperiment.jsonv2 && !go1.27

// Go 1.25 and 1.26 carry encoding/json/v2 only as an experiment, outside any
// versioned API, so this file needs no version of its own. See v2.go.

package jsonenc

import (
	jsonv2 "encoding/json/v2"
)

// Variant reports which JSON implementation backs this build. zip.New()
// logs this once at startup so operators can confirm v2 is active.
const Variant = "encoding/json/v2"

// v1Wire holds the three places v2 writes different bytes from v1 that callers
// can see: map key order, and nil maps and slices as {} and [] rather than null.
// Go 1.27 turns v2 on by default, so without these the same app answers REST
// and MCP differently depending on the toolchain that built it.
var v1Wire = jsonv2.JoinOptions(
	jsonv2.Deterministic(true),
	jsonv2.FormatNilMapAsNull(true),
	jsonv2.FormatNilSliceAsNull(true),
)

// Marshal encodes v as JSON via encoding/json/v2, byte-compatible with v1.
func Marshal(v any) ([]byte, error) { return jsonv2.Marshal(v, v1Wire) }

// Unmarshal decodes data into v via encoding/json/v2.
func Unmarshal(data []byte, v any) error { return jsonv2.Unmarshal(data, v) }
