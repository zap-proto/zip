// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"sort"
	"strconv"
	"strings"
)

// The ZAP IDL, read off a [Manifest].
//
// Projection #7, and the one that is allowed to REFUSE: a field with no fixed
// form is not silently dropped, it is named in the ledger under the reason it
// cannot cross. That is why the IDL is an output and never the input — a
// description that could not state a map could not report one either, and the
// unrepresentable field would become undeclarable instead of published.
//
// The offsets are not computed here. They come from the manifest, where the
// front end put them, because the front end is also what generates the
// accessors that read against them: Go derives them at run time from the type,
// Rust and C++ derive them at compile time. One rule, three places it is
// applied, and a byte comparison of the three schemas is what proves they
// applied it the same way.

// ProjectZAP is m as a ZAP schema.
func ProjectZAP(pkg string, ms ...Manifest) *Schema {
	s := &Schema{Package: pkg}
	e := &idl{schema: s, named: map[string]string{}, taken: map[string]bool{}}
	for i, m := range ms {
		e.types = newProjector(m)
		ops := append([]ManifestOp(nil), m.Ops...)
		sort.Slice(ops, func(i, j int) bool { return ops[i].ID < ops[j].ID })

		name := m.App
		if name == "" {
			if len(ms) == 1 {
				name = pkg
			} else {
				name = "app" + strconv.Itoa(i)
			}
		}
		iface := &Interface{Name: idlName(name)}
		e.iface = iface
		e.methods = map[string]bool{}
		for _, op := range ops {
			if idlName(op.ID) == op.ID {
				e.methods[op.ID] = true
			}
		}
		for _, op := range ops {
			e.method(op)
		}
		if len(iface.Methods) > 0 {
			s.Interfaces = append(s.Interfaces, iface)
		}
	}
	sortSchema(s)
	return s
}

// idl carries one file's naming state while the manifests are walked.
type idl struct {
	schema  *Schema
	types   *projector
	named   map[string]string // type id → the name it is declared under, "" for refused
	taken   map[string]bool
	iface   *Interface
	methods map[string]bool
	op      string
}

func (e *idl) method(op ManifestOp) {
	e.op = op.ID
	req, reqOK := e.payload(op.In)
	rep, repOK := e.payload(op.Out)
	if !reqOK || !repOK {
		return
	}
	e.iface.Methods = append(e.iface.Methods, &Method{
		Name: e.methodName(op.ID), Request: req, Reply: rep, Doc: op.Description,
	})
}

func (e *idl) methodName(id string) string {
	if name := idlName(id); name == id {
		return id
	}
	name := idlName(id)
	for n := 2; e.methods[name]; n++ {
		name = idlName(id) + strconv.Itoa(n)
	}
	e.methods[name] = true
	e.schema.Renamed = append(e.schema.Renamed, Rename{Op: id, Method: name})
	return name
}

// payload names the struct one direction carries, "" for a direction that
// carries nothing. The bool is whether it could be expressed at all.
func (e *idl) payload(id string) (string, bool) {
	td := e.types.types[id]
	if td == nil || td.Kind != "struct" || len(td.Fields) == 0 {
		return "", true
	}
	name := e.define(td)
	if name == "" {
		return "", false
	}
	return name, true
}

// define declares td and returns its name, or "" when it cannot be declared.
func (e *idl) define(td *TypeDesc) string {
	if name, ok := e.named[td.ID]; ok {
		if name == "" {
			e.gap(td.Name, td.Spell, CauseReaches) // re-blame for THIS op
		}
		return name
	}
	lay, ok := e.types.layout(td, map[string]bool{})
	if !ok {
		e.named[td.ID] = ""
		e.diagnose(td)
		return ""
	}
	if len(lay.slots) == 0 {
		e.named[td.ID] = ""
		e.gap(td.Name, td.Spell, CauseEmpty)
		return ""
	}

	name := e.name(td)
	e.named[td.ID] = name
	e.taken[name] = true

	fields := make([]Field, 0, len(lay.slots))
	for i, s := range lay.slots {
		f := td.Fields[i]
		fields = append(fields, Field{Name: idlName(f.Name), Type: s.repr, Offset: s.at})
		e.opacity(name, f, s)
		if strings.HasPrefix(s.repr, "bytes_fixed[") || strings.HasPrefix(s.elem, "bytes_fixed[") {
			e.schema.Coded = append(e.schema.Coded, Coded{Struct: name, Field: f.Name, Type: s.repr})
		}
		e.dropped(name, f)
	}
	e.schema.Structs = append(e.schema.Structs, &Struct{
		Name: name, Fields: fields, Size: lay.size, From: td.Spell,
	})
	return name
}

// diagnose names EVERY field that cannot cross, and why.
//
// The layout stops at the first field it refuses, which is the answer an encoder
// needs; a work list has to name them all, or it understates the migration by
// however many problems each type has.
func (e *idl) diagnose(td *TypeDesc) {
	for _, f := range td.Fields {
		if _, ok := e.types.slotOf(f.Type, map[string]bool{}); ok {
			continue // this field is fine; another one is why the type refused.
		}
		e.gap(td.Name+"."+f.Name, f.Spell, e.cause(f.Type))
	}
}

// cause is why a field has no fixed form, from the vocabulary the ledger groups
// by. It asks the description, which is what the front end's own layout already
// refused on.
func (e *idl) cause(r TypeRef) string {
	switch {
	case r.Map != nil:
		return CauseMap
	case r.List != nil:
		return e.cause(*r.List)
	case r.Ref != "":
		td := e.types.types[r.Ref]
		if td == nil {
			return CauseAny
		}
		switch td.Kind {
		case "map":
			return CauseMap
		case "struct":
			return CauseReaches
		}
		return CauseUnwirable
	case r.Prim == "any":
		return CauseAny
	}
	return CauseUnwirable
}

// opacity records a field whose bytes are exact and whose TYPE NAME is lost: a
// nested value crosses as `bytes`, so the schema says something is there and
// not what it is.
func (e *idl) opacity(decl string, f FieldDesc, s slot) {
	inner := func(r TypeRef) *TypeDesc {
		if r.Ref == "" {
			return nil
		}
		return e.types.types[r.Ref]
	}
	switch {
	case s.repr == "bytes":
		if td := inner(f.Type); td != nil && td.Kind == "struct" {
			e.schema.Opaque = append(e.schema.Opaque, Opacity{Struct: decl, Field: f.Name, Go: td.Spell})
		}
	case s.elem == "bytes" && f.Type.List != nil:
		if td := inner(*f.Type.List); td != nil && td.Kind == "struct" {
			e.schema.Opaque = append(e.schema.Opaque, Opacity{Struct: decl, Field: f.Name, Go: td.Spell, List: true})
		}
	}
}

// dropped names a field whose VALUE does not cross on a type the layout
// accepted: a nested value with no slots of its own crosses as an empty message.
func (e *idl) dropped(decl string, f FieldDesc) {
	r := f.Type
	if r.List != nil {
		r = *r.List
	}
	td := e.types.types[r.Ref]
	if td == nil || td.Kind != "struct" || len(td.Fields) > 0 {
		return
	}
	e.schema.Dropped = append(e.schema.Dropped, Loss{
		Struct: decl, Field: f.Name, Go: td.Spell, Cause: LossEmpty,
	})
}

// name is the IDL name for a described type: its own name, qualified by its
// package when a different type already holds that name.
func (e *idl) name(td *TypeDesc) string {
	base := idlName(td.Name)
	if base == "" || base == "_" {
		base = idlName(e.op) + "_anon"
	}
	if !e.taken[base] {
		return base
	}
	if p := td.Pkg; p != "" {
		base = idlName(p[strings.LastIndexByte(p, '/')+1:]) + "_" + base
	}
	for name, n := base, 2; ; n++ {
		if !e.taken[name] {
			return name
		}
		name = base + strconv.Itoa(n)
	}
}

func (e *idl) gap(field, spell, cause string) {
	e.schema.Gaps = append(e.schema.Gaps, Gap{Op: e.op, Field: field, Go: spell, Cause: cause})
}

// sortSchema puts a schema in the one order it renders in, so the same program
// writes the same bytes every run.
func sortSchema(s *Schema) {
	sort.Slice(s.Structs, func(i, j int) bool { return s.Structs[i].Name < s.Structs[j].Name })
	sort.Slice(s.Interfaces, func(i, j int) bool { return s.Interfaces[i].Name < s.Interfaces[j].Name })
	sort.Slice(s.Gaps, func(i, j int) bool {
		if s.Gaps[i].Op != s.Gaps[j].Op {
			return s.Gaps[i].Op < s.Gaps[j].Op
		}
		return s.Gaps[i].Field < s.Gaps[j].Field
	})
	sort.Slice(s.Opaque, func(i, j int) bool {
		if s.Opaque[i].Struct != s.Opaque[j].Struct {
			return s.Opaque[i].Struct < s.Opaque[j].Struct
		}
		return s.Opaque[i].Field < s.Opaque[j].Field
	})
	sort.Slice(s.Dropped, func(i, j int) bool {
		if s.Dropped[i].Struct != s.Dropped[j].Struct {
			return s.Dropped[i].Struct < s.Dropped[j].Struct
		}
		return s.Dropped[i].Field < s.Dropped[j].Field
	})
	sort.Slice(s.Coded, func(i, j int) bool {
		if s.Coded[i].Struct != s.Coded[j].Struct {
			return s.Coded[i].Struct < s.Coded[j].Struct
		}
		return s.Coded[i].Field < s.Coded[j].Field
	})
}
