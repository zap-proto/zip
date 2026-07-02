package runtime

import "github.com/zap-proto/zip/internal/jsonenc"

// jsonMarshal / jsonUnmarshal route through zip's jsonenc package so the
// embedded-JS bridge uses the same JSON impl as the edge (encoding/json,
// or encoding/json/v2 under GOEXPERIMENT=jsonv2) — one wire format, one
// way to encode it.
func jsonMarshal(v any) ([]byte, error)   { return jsonenc.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return jsonenc.Unmarshal(b, v) }
