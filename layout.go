// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

// Where a value sits in a ZAP message, derived from what the value IS.
//
// The rules are [zapenc.LayoutOf]'s, restated over a manifest instead of over
// reflect.Type: a field is aligned to its own width and takes that many bytes,
// the message is rounded up to 8, and bytes_fixed[N] is the exception — N bytes
// INLINE, aligned to 1, so an id sits immediately after the u32 before it.
//
// Restated, not re-decided. TestLayoutAgreesWithTheEncoder holds every offset,
// width and spelling here against the encoder's own answer for the same type, so
// the two cannot drift: this file describes the wire the Go encoder writes, and a
// C++ or Rust service derives the same offsets from its own declarations at
// compile time, where Go derives them at run time. That asymmetry is the whole
// reason the derivation is stated over a manifest — a language with no run-time
// type graph still has a layout, it simply knows it earlier.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zap-proto/zip/manifest"
)

// slot is one field's place: what it is called, where it sits, and what the IDL
// names it. Field is the declaration it came from, which is what a diagnosis
// needs and the emitted text does not.
type slot struct {
	Name   string
	Offset int
	Width  int
	Type   string // the .zap type: u64, text, bytes, bytes_fixed[32], list<…>
	N      int    // bytes_fixed[N]: the length. Otherwise 0.
	Elem   string // list<…>: the element's .zap type
	Field  manifest.Field
}

// layout is a whole message: its slots in declaration order, and its size.
type layout struct {
	Slots []slot
	Size  int
}

// layoutOf derives t's layout, or says which field has no wire form. inside is
// the set of declarations already being derived: a type that contains itself
// asks for its own layout while computing it, and a field IS an offset and a
// width, so a recursive type has no bounded width and no layout to derive.
func layoutOf(m *manifest.App, t *manifest.Type, inside map[string]bool) (layout, error) {
	if t == nil || t.Kind != manifest.Record {
		return layout{}, fmt.Errorf("zip: %s is not a struct", spellType(t))
	}
	if inside[t.Ref] {
		return layout{}, fmt.Errorf("zip: %s contains itself", spellType(t))
	}
	inside[t.Ref] = true
	defer delete(inside, t.Ref)

	var lay layout
	off := 0
	for _, f := range m.Own(t) {
		if f.Private {
			// A field the declaration keeps to itself crosses nowhere and is
			// nobody's contract. Skipping it is right; consuming a slot for it
			// would not be.
			continue
		}
		s, err := slotOf(m, &f.Type, inside)
		if err != nil {
			return layout{}, fmt.Errorf("%s.%s: %w", t.Name, f.Name, err)
		}
		s.Name, s.Field = f.Name, f
		off = align(off, s.align())
		s.Offset = off
		off += s.Width
		lay.Slots = append(lay.Slots, s)
	}
	lay.Size = align(off, 8)
	return lay, nil
}

// align is where a slot may begin: its own width, except for bytes_fixed, whose
// bytes are inline and align to 1.
func (s slot) align() int {
	if s.N > 0 {
		return 1
	}
	return s.Width
}

func align(off, n int) int { return (off + n - 1) &^ (n - 1) }

// slotOf is what one value takes on the wire. Everything variable — text, bytes,
// a list, a nested value — carries an offset and a length in 8 bytes.
func slotOf(m *manifest.App, t *manifest.Type, inside map[string]bool) (slot, error) {
	if t == nil {
		return slot{}, fmt.Errorf("zip: a field with no type has no wire form")
	}
	switch t.Kind {
	case manifest.Bool:
		return slot{Type: "bool", Width: 1}, nil
	case manifest.Int, manifest.Uint, manifest.Float:
		name, w := number(t)
		if name == "" {
			return slot{}, fmt.Errorf("zip: %s has no width", spellType(t))
		}
		return slot{Type: name, Width: w}, nil
	case manifest.String:
		return slot{Type: "text", Width: 8}, nil
	case manifest.Record:
		// A nested value crosses as a complete message in an {offset,length}
		// slot, so it is "bytes" and not "struct". Its own layout is derived all
		// the same: a nested value that cannot cross means this one cannot.
		if _, err := layoutOf(m, t, inside); err != nil {
			return slot{}, err
		}
		return slot{Type: "bytes", Width: 8}, nil
	case manifest.Fixed:
		// bytes_fixed[N]. Only a byte array: an array of anything else has no
		// inline wire form, and a list is how a sequence crosses.
		if !isByte(t.Elem) {
			return slot{}, fmt.Errorf("zip: an array of %s has no wire form; use a slice", spellType(t.Elem))
		}
		return slot{Type: "bytes_fixed[" + strconv.Itoa(t.Len) + "]", Width: t.Len, N: t.Len}, nil
	case manifest.List:
		if isByte(t.Elem) {
			return slot{Type: "bytes", Width: 8}, nil
		}
		el, err := slotOf(m, t.Elem, inside)
		if err != nil {
			return slot{}, err
		}
		if el.Type == "" || el.Elem != "" {
			return slot{}, fmt.Errorf("zip: a slice of slices has no wire form; wrap the inner one in a struct")
		}
		return slot{Type: "list<" + el.Type + ">", Width: 8, Elem: el.Type}, nil
	case manifest.Table:
		return slot{}, fmt.Errorf("zip: %s has no wire form: a key set is not a layout", spellType(t))
	case manifest.Any:
		return slot{}, fmt.Errorf("zip: a field that names no type has no wire form")
	}
	return slot{}, fmt.Errorf("zip: %s cannot cross the plane; give it a type that can", t.Kind)
}

// number is the IDL's name and width for a number. The width comes from the
// format the manifest carries, because "integer" alone is not a layout: a
// generated type that reads uint32 as int64 takes eight bytes where the service
// laid four, and every field after it is read from the wrong place.
func number(t *manifest.Type) (string, int) {
	switch t.Format {
	case "int8":
		return "i8", 1
	case "int16":
		return "i16", 2
	case "int32":
		return "i32", 4
	case "int64":
		return "i64", 8
	case "uint8":
		return "u8", 1
	case "uint16":
		return "u16", 2
	case "uint32":
		return "u32", 4
	case "uint64":
		return "u64", 8
	case "float":
		return "f32", 4
	case "double":
		return "f64", 8
	}
	return "", 0
}

func isByte(t *manifest.Type) bool {
	return t != nil && t.Kind == manifest.Uint && t.Format == "uint8"
}

// spellType names a type the way a person would grep for it, which is what the
// ledger is read for: someone about to go and change that declaration. A
// declared type is its name qualified by where it was declared; everything else
// is spelled from what it is made of.
func spellType(t *manifest.Type) string {
	if t == nil {
		return "nil"
	}
	if t.Name != "" {
		if p := t.Pkg; p != "" {
			return base(p) + "." + t.Name
		}
		return t.Name
	}
	switch t.Kind {
	case manifest.List:
		return "[]" + spellType(t.Elem)
	case manifest.Fixed:
		return "[" + strconv.Itoa(t.Len) + "]" + spellType(t.Elem)
	case manifest.Table:
		return "map[" + spellType(t.Key) + "]" + spellType(t.Elem)
	case manifest.Any:
		return "interface {}"
	case manifest.Record:
		return "struct"
	}
	return string(t.Kind)
}

// base is the last segment of a declaration's home — the package, module or
// namespace it was declared in, as a reader would say it out loud. One
// separator, because a front end writes the home it wants read back: a Go import
// path, or the C++ namespace "info".
func base(pkg string) string {
	return pkg[strings.LastIndexByte(pkg, '/')+1:]
}
