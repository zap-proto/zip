// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"strings"
)

// Where a field sits, derived once for every language.
//
// A ZAP field IS an offset and a width, and the rule that assigns them is the
// same rule wherever the type was declared: walk the fields in declaration
// order, align each to its own width, and round the fixed section up to eight.
// Go's encoder derives it at run time from a reflect.Type (internal/zapenc), and
// it has to, because a Go value is encoded reflectively. Nothing else needs to:
// a description already says what each field is, so the offsets follow from the
// description alone.
//
// So the rule lives HERE, once, and the .zap a Rust service publishes is laid
// out by the same code that lays out a Go one. That the two agree with the Go
// ENCODER is not assumed either — TestProjectZAPMatchesRegistry compares this
// against the layout zapenc computes over the same types, which is the only way
// a schema and the bytes it describes stay one thing.

// slot is one field's place in the fixed section.
type slot struct {
	at   int
	repr string // the IDL's spelling: bool, u32, text, bytes, list<...>, bytes_fixed[N]
	elem string // a list's element spelling
}

// layout is a struct's fixed section: one slot per field, in declaration order.
type layoutOut struct {
	slots []slot
	size  int
}

// layout assigns td's fields their slots, or names the first field that has no
// fixed form at all.
func (p *projector) layout(td *TypeDesc, inside map[string]bool) (layoutOut, bool) {
	if inside[td.ID] {
		return layoutOut{}, false // a field is an offset and a width; a recursive type has none.
	}
	inside[td.ID] = true
	defer delete(inside, td.ID)

	out := layoutOut{}
	at := 0
	for _, f := range td.Fields {
		s, ok := p.slotOf(f.Type, inside)
		if !ok {
			return layoutOut{}, false
		}
		w := slotWidth(s.repr)
		a := w
		if n, fixed := fixedLen(s.repr); fixed {
			a, w = 1, n
		}
		at = (at + a - 1) &^ (a - 1)
		s.at = at
		at += w
		out.slots = append(out.slots, s)
	}
	out.size = (at + 7) &^ 7
	return out, true
}

// slotOf is one field's spelling and whether it has a fixed form at all.
func (p *projector) slotOf(r TypeRef, inside map[string]bool) (slot, bool) {
	switch {
	case r.Map != nil:
		return slot{}, false // no map in the IDL: a key set is not a layout.
	case r.List != nil:
		if r.Len > 0 {
			// A fixed-length list of anything but bytes has no wire form: an
			// element is one value at one width, and a run of them inline is not
			// a slot this layout has.
			return slot{}, false
		}
		e, ok := p.elemOf(*r.List, inside)
		if !ok {
			return slot{}, false
		}
		return slot{repr: "list<" + e + ">", elem: e}, true
	case r.Ref != "":
		td := p.types[r.Ref]
		if td == nil {
			return slot{}, false
		}
		switch td.Kind {
		case "struct":
			// A nested value crosses as a complete message in an {offset,length}
			// slot, so it is `bytes` here — and only if it lays out at all.
			if _, ok := p.layout(td, inside); !ok {
				return slot{}, false
			}
			return slot{repr: "bytes"}, true
		case "list":
			if td.Len > 0 {
				return slot{}, false
			}
			e, ok := p.elemOf(*td.Elem, inside)
			if !ok {
				return slot{}, false
			}
			return slot{repr: "list<" + e + ">", elem: e}, true
		case "map":
			return slot{}, false
		}
		return primSlot(td.Repr)
	}
	return primSlot(r.Prim)
}

// elemOf is a list element's spelling. A list of lists has no form: the element
// of a list is one value at one width, and a list is neither.
func (p *projector) elemOf(r TypeRef, inside map[string]bool) (string, bool) {
	s, ok := p.slotOf(r, inside)
	if !ok || strings.HasPrefix(s.repr, "list<") {
		return "", false
	}
	return s.repr, true
}

// primSlot is a primitive's spelling, or a refusal for one that has none.
func primSlot(prim string) (slot, bool) {
	switch prim {
	case "bool", "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "f32", "f64", "bytes", "text":
		return slot{repr: prim}, true
	case "string":
		return slot{repr: "text"}, true
	}
	if _, ok := fixedLen(prim); ok {
		return slot{repr: prim}, true
	}
	return slot{}, false
}

// slotWidth is the fixed-area size of one spelling. Everything variable — text,
// bytes, a list, a nested value — carries an offset and a length.
func slotWidth(repr string) int {
	switch {
	case repr == "bool" || repr == "i8" || repr == "u8":
		return 1
	case repr == "i16" || repr == "u16":
		return 2
	case repr == "i32" || repr == "u32" || repr == "f32":
		return 4
	}
	if n, ok := fixedLen(repr); ok {
		return n
	}
	return 8
}
