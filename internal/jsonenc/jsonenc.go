package jsonenc

import (
	"encoding/json"
)

// Variant names the JSON implementation. zip.New() logs it once at startup.
const Variant = "encoding/json"

// Marshal encodes v as JSON.
func Marshal(v any) ([]byte, error) { return json.Marshal(v) }

// Unmarshal decodes data into v.
func Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
