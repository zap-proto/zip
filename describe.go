// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

// Go's answer to "what does this app declare".
//
// It is the NINTH projection and the one the other eight are computed from. Go
// reads its own declarations by reflection — that is Go's extractor, the way a
// macro is Rust's and a source pass is C++'s — and everything downstream reads
// the [manifest.App] this produces. So reflect.Type stops here: nothing past
// this file asks Go what a type is, which is what lets a service written in
// another language have the same document, tool list, CLI and schema.

import (
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/zap-proto/zip/manifest"
)

// Manifest is what this app declares, said without Go.
//
// Every projection reads it: [App.OpenAPISpec], [App.MCPTools], [App.Commands],
// [ZAPSchema] and the SDK writers all reduce over this value. Two apps that
// produce the same manifest produce the same document, whatever language either
// was written in — which is the whole test that zip is one framework with
// several front ends rather than one framework and several clients.
func (a *App) Manifest() *manifest.App {
	m := &manifest.App{
		Name:        a.cfg.AppName,
		Title:       a.cfg.OpenAPI.Title,
		Description: a.cfg.OpenAPI.Description,
		Version:     a.cfg.OpenAPI.Version,
		Structs:     map[string]manifest.Struct{},
	}
	d := &describer{app: m, ref: map[reflect.Type]string{}}

	ops := append([]*registeredOp(nil), a.Registry()...)
	// Sorted by address, which is the order every projection reads them in
	// anyway. A manifest ordered by registration would churn on an edit that
	// moved a line and changed nothing a client can see.
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	for _, op := range ops {
		m.Ops = append(m.Ops, d.op(op))
	}
	return m
}

// describer walks Go types once each. ref is the key a type is already filed
// under, which is also the cycle guard: a type that contains itself finds its own
// key claimed and refers to it instead of walking forever.
type describer struct {
	app  *manifest.App
	ref  map[reflect.Type]string
	anon int
}

func (d *describer) op(op *registeredOp) manifest.Op {
	out := manifest.Op{
		Method:   op.Method,
		Path:     op.Path,
		ID:       op.OperationID,
		Summary:  op.Summary,
		Tags:     op.Tags,
		Statuses: op.Statuses,
		Headers:  op.ResponseHeaders,
		Pkg:      op.Pkg,
		Origin:   op.Origin,
		Held:     op.rule != nil && op.rule() != nil,
		In:       d.typ(op.InType),
		Out:      d.typ(op.OutType),
	}
	if doc, has := docFor(op.Pkg, op.Method, op.Path); has {
		out.Doc = &manifest.Doc{
			Description: doc.Description,
			Fields:      doc.Fields,
			Example:     doc.Example,
			Response:    doc.Response,
		}
	}
	return out
}

// typ describes t. A pointer is followed: the wire carries the value, and every
// projection has always read through the pointer to find it.
func (d *describer) typ(t reflect.Type) *manifest.Type {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := &manifest.Type{
		Name: typeName(t),
		Pkg:  t.PkgPath(),
		Text: readsText(t),
	}
	// A type that writes its own JSON is not described by what it is made of.
	// That is read first, because the rule is about the marshaler and not about
	// being a struct — json.RawMessage is a []byte whose JSON is raw JSON.
	if isMarshaler(t) {
		out.States = true
		out.Schema = declaredSchema(t)
		if out.Schema == nil && t == timeType {
			// The one marshaler whose output shape is documented: RFC 3339.
			// Stated here rather than special-cased downstream, so a projection
			// has one rule — a stated schema, or none.
			out.Schema = map[string]any{"type": "string", "format": "date-time"}
		}
	}
	switch t.Kind() {
	case reflect.String:
		out.Kind = manifest.String
	case reflect.Bool:
		out.Kind = manifest.Bool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out.Kind, out.Format = manifest.Int, numberFormat(t.Kind())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		out.Kind, out.Format = manifest.Uint, numberFormat(t.Kind())
	case reflect.Float32, reflect.Float64:
		out.Kind, out.Format = manifest.Float, numberFormat(t.Kind())
	case reflect.Slice:
		out.Kind, out.Elem = manifest.List, d.typ(t.Elem())
	case reflect.Array:
		out.Kind, out.Elem, out.Len = manifest.Fixed, d.typ(t.Elem()), t.Len()
	case reflect.Map:
		out.Kind, out.Elem, out.Key = manifest.Table, d.typ(t.Elem()), d.typ(t.Key())
	case reflect.Struct:
		out.Kind, out.Ref = manifest.Record, d.record(t)
	case reflect.Interface:
		out.Kind = manifest.Any
	default:
		// A channel, a function, a complex number: a value Go has and no wire
		// carries. It is spelled as what it is, and every projection treats a
		// kind it does not recognise as having no wire form.
		out.Kind = manifest.Kind(t.Kind().String())
	}
	return out
}

// record files t's fields under a key and answers with the key. The key is
// claimed BEFORE the fields are walked — that claim is what a recursive type
// finds instead of recursing.
func (d *describer) record(t reflect.Type) string {
	if key, ok := d.ref[t]; ok {
		return key
	}
	name := typeName(t)
	key := manifest.Ref(t.PkgPath(), name)
	if key == "" {
		// Anonymous: nothing to name it after. It is filed under an ordinal in
		// walk order, which nothing publishes — an anonymous struct is inlined
		// wherever it is described, having no name to be referred to by.
		d.anon++
		key = "anon" + strconv.Itoa(d.anon)
	}
	for _, taken := d.app.Structs[key]; taken; _, taken = d.app.Structs[key] {
		// Two DIFFERENT types under one key: the same declared name in one
		// package, which reflect can produce for a type built at run time. The
		// second gets its own key rather than overwriting the first's fields.
		key += "'"
	}
	d.ref[t] = key
	d.app.Structs[key] = manifest.Struct{} // claimed, so the walk below terminates

	s := manifest.Struct{Name: name, Pkg: t.PkgPath()}
	// What the body carries: encoding/json's fields, an embedded struct's
	// promoted among them.
	for _, f := range wireFields(t) {
		s.Body = append(s.Body, manifest.Field{
			Name:     f.Name,
			JSON:     jsonFieldName(f),
			URL:      urlFieldName(f),
			Header:   headerFieldName(f),
			Required: strings.Contains(f.Tag.Get("validate"), "required"),
			Type:     *d.typ(f.Type),
		})
	}
	// What a layout slots: this declaration's own fields, promoting nothing. An
	// unexported one is carried and marked, because a layout has to know the
	// name it is NOT giving a slot to — that is how a value promoted onto the
	// JSON wire and absent from this one is reported rather than lost.
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		s.Own = append(s.Own, manifest.Field{
			Name:    f.Name,
			JSON:    jsonFieldName(f),
			Embed:   f.Anonymous,
			Private: !f.IsExported(),
			Type:    *d.typ(f.Type),
		})
	}
	d.app.Structs[key] = s
	return key
}

// describeType describes one type on its own, in a manifest of its own. It is
// for a type zip itself publishes — the held body — which belongs to no app and
// must read the same in every app's document.
func describeType(t reflect.Type) (*manifest.App, *manifest.Type) {
	m := &manifest.App{Structs: map[string]manifest.Struct{}}
	d := &describer{app: m, ref: map[reflect.Type]string{}}
	return m, d.typ(t)
}
