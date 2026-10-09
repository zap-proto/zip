// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// A union is a type with a OneOf method. The method's RESULT TYPES are the
// alternatives, and nothing calls it: the signature is the declaration.
//
//	// Ingest is one event, a list of them, or a batch envelope.
//	type Ingest struct{ … }
//	func (Ingest) OneOf() (Event, []Event, Batch) { return Event{}, nil, Batch{} }
//
// The document publishes oneOf over the alternatives, for a request body and
// for an answer alike, each alternative described exactly as it would be alone.
// The type decodes and encodes itself (UnmarshalJSON, MarshalJSON, or [Parser]
// for a request), because which alternative a body is belongs to the type.
//
// An answer's alternative may state its own status with a value-receiver
// StatusCode, which is read off its zero value: the document files the
// alternative under that status, and the op declares it. [Or] is the union
// zip writes for itself, and the one whose alternatives may be streams.

// alternatives are a union type's alternatives, or nil.
func alternatives(t reflect.Type) []reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return nil
	}
	m, ok := t.MethodByName("OneOf")
	if !ok {
		m, ok = reflect.PointerTo(t).MethodByName("OneOf")
	}
	if !ok || m.Type.NumIn() != 1 || m.Type.NumOut() < 2 {
		return nil
	}
	out := make([]reflect.Type, m.Type.NumOut())
	for i := range out {
		out[i] = m.Type.Out(i)
	}
	return out
}

// Or is one of two answers, or one of two request bodies: exactly one of A and
// B is set. Or[Out, Sse[T]] is an op that answers one JSON value or an event
// stream; Or[Created, Existing] files each under the status it states.
//
// As a request body it decodes into the alternative the JSON is: an
// alternative whose kind (object, array, string, number, boolean) matches is
// tried first, refusing unknown fields, A before B; then each is tried as
// encoding/json would read it.
type Or[A, B any] struct {
	// A is the first alternative, when it is the one this value holds.
	A *A
	// B is the second alternative, when it is the one this value holds.
	B *B
}

// OneOf declares the alternatives.
func (Or[A, B]) OneOf() (A, B) {
	var a A
	var b B
	return a, b
}

// isOr reports whether t is an [Or], whose alternatives the seam unwraps at
// run time; any other union is a value that writes itself.
func isOr(t reflect.Type) bool {
	_, ok := reflect.New(t).Interface().(interface{ either() any })
	return ok
}

// either is the alternative the value holds, nil when it holds none.
func (o Or[A, B]) either() any {
	switch {
	case o.A != nil:
		return o.A
	case o.B != nil:
		return o.B
	}
	return nil
}

// MarshalJSON writes the alternative the value holds, null when it holds none.
func (o Or[A, B]) MarshalJSON() ([]byte, error) {
	v := o.either()
	if v == nil {
		return []byte("null"), nil
	}
	return json.Marshal(v)
}

// UnmarshalJSON reads whichever alternative the JSON is.
func (o *Or[A, B]) UnmarshalJSON(data []byte) error {
	o.A, o.B = nil, nil
	if string(bytes.TrimSpace(data)) == "null" {
		return nil
	}
	var a A
	var b B
	kind := jsonKind(data)
	if kindJSON(reflect.TypeOf(&a).Elem()) == kind && strict(data, &a) == nil {
		o.A = &a
		return nil
	}
	if kindJSON(reflect.TypeOf(&b).Elem()) == kind && strict(data, &b) == nil {
		o.B = &b
		return nil
	}
	a, b = *new(A), *new(B)
	errA := json.Unmarshal(data, &a)
	if errA == nil {
		o.A = &a
		return nil
	}
	if json.Unmarshal(data, &b) == nil {
		o.B = &b
		return nil
	}
	return fmt.Errorf("the value is neither alternative: %w", errA)
}

// strict decodes refusing a field the type does not have.
func strict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// jsonKind is the kind of a JSON value by its first byte.
func jsonKind(data []byte) string {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return ""
	}
	switch data[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	}
	return "number"
}

// kindJSON is the kind of JSON value a type reads.
func kindJSON(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isMarshaler(t) || readsText(t) {
		switch schemaOf(t, nil, nil)["type"] {
		case "integer", "number":
			return "number"
		case "boolean", "object", "array":
			return schemaOf(t, nil, nil)["type"].(string)
		}
		return "string"
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return "object"
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return "string"
		}
		return "array"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Interface:
		return ""
	}
	return "number"
}
