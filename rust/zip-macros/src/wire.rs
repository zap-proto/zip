// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! `#[derive(Wire)]` — a type that says what it is.
//!
//! A proc-macro sees only the item it is written on, so the op macro learns a
//! parameter's NAME and nothing else. The description has to travel with the
//! type, and this is how: the derive emits the type's own field list, wire
//! names and prose, once, where the type is declared, and everything that
//! refers to the type refers to that.
//!
//! It emits two readers of one description. The `describe` a running service
//! binds a request with, and the fragment `zipc` projects — both from this one
//! pass, so what the document says a field is called is what the binder reads
//! it under.

use proc_macro2::TokenStream;
use quote::{format_ident, quote};
use syn::{Data, DeriveInput, Fields, Type};

use crate::attr::Attrs;
use crate::{doc, frag, ty};

pub fn derive(input: DeriveInput) -> Result<TokenStream, syn::Error> {
    let name = input.ident.clone();
    let id = name.to_string();
    let own = Attrs::read(&input.attrs)?;
    let prose = doc::read(&input.attrs);

    let Data::Struct(s) = &input.data else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            "zip::Wire describes a struct: a value with fields, or a newtype that is one value",
        ));
    };

    match &s.fields {
        Fields::Named(named) => {
            let fields: Vec<Field> = named
                .named
                .iter()
                .map(Field::read)
                .collect::<Result<_, _>>()?;
            frag::write("types", &id, &record(&id, &prose.text, &fields));
            Ok(shape(&name, &id, &fields))
        }
        Fields::Unnamed(un) if un.unnamed.len() == 1 => {
            let inner = &un.unnamed[0].ty;
            frag::write("types", &id, &value(&id, &own, inner));
            Ok(newtype(&name, &id, &own, inner))
        }
        _ => Err(syn::Error::new_spanned(
            &input.ident,
            "zip::Wire describes a struct with named fields, or a newtype around one value",
        )),
    }
}

/// Field is one described field.
struct Field {
    name: syn::Ident,
    json: String,
    url: String,
    header: String,
    required: bool,
    doc: String,
    ty: Type,
    reference: ty::Ref,
}

impl Field {
    fn read(f: &syn::Field) -> Result<Self, syn::Error> {
        let a = Attrs::read(&f.attrs)?;
        let name = f.ident.clone().expect("named field");
        let json = if a.skip {
            "-".to_string()
        } else {
            a.json.clone().unwrap_or_else(|| name.to_string())
        };
        Ok(Field {
            json,
            url: a.url.clone().unwrap_or_default(),
            header: a.header.clone().unwrap_or_default(),
            required: a.required,
            doc: doc::read(&f.attrs).text,
            reference: ty::of(&f.ty),
            ty: f.ty.clone(),
            name,
        })
    }
}

/// record is the fragment for a struct.
fn record(id: &str, prose: &str, fields: &[Field]) -> String {
    let items: Vec<String> = fields
        .iter()
        .map(|f| {
            let mut o = frag::Object::new();
            o.text("name", &f.name.to_string())
                .text("json", &f.json)
                .text("url", &f.url)
                .text("header", &f.header)
                .flag("required", f.required)
                .text("doc", &f.doc)
                .raw("type", &f.reference.json)
                .text("spell", &f.reference.spell);
            o.finish()
        })
        .collect();
    let mut o = frag::Object::new();
    o.text("id", id)
        .text("name", id)
        .text("kind", "struct")
        .text("spell", id)
        .text("doc", prose)
        .raw("fields", &frag::list(&items));
    o.finish()
}

/// value is the fragment for a newtype: one value, whose wire form is its own
/// business. It is the escape hatch the study named — a type whose JSON is not
/// what it is made of has to say so, and this is where it says it.
fn value(id: &str, a: &Attrs, inner: &Type) -> String {
    let mut o = frag::Object::new();
    o.text("id", id)
        .text("name", id)
        .text("kind", "scalar")
        .text("spell", id)
        .text("repr", ty::prim(inner).unwrap_or("any"))
        .flag("text", a.text);
    if let Some(j) = a.schema() {
        o.raw("json", j);
    } else if a.text {
        o.raw("json", "{\"type\":\"string\"}");
    }
    o.finish()
}

/// shape is the impl for a struct: the description a binder reads, and the JSON
/// the wire carries.
fn shape(name: &syn::Ident, id: &str, fields: &[Field]) -> TokenStream {
    let descs = fields.iter().map(|f| {
        let n = f.name.to_string();
        let j = &f.json;
        let u = &f.url;
        let h = &f.header;
        let req = f.required;
        let scalar = scalar_of(&f.ty);
        quote! {
            ::zip::FieldDesc { name: #n, json: #j, url: #u, header: #h, required: #req, scalar: #scalar }
        }
    });

    let writes = fields
        .iter()
        .filter(|f| f.json != "-")
        .enumerate()
        .map(|(i, f)| {
            let j = &f.json;
            let at = &f.name;
            let lead = if i == 0 { "\"" } else { ",\"" };
            let key = format!("{lead}{j}\":");
            let w = write_of(&f.ty, quote!(&self.#at));
            quote! { out.push_str(#key); #w; }
        });

    let reads = fields.iter().map(|f| {
        let at = &f.name;
        if f.json == "-" {
            let d = default_of(&f.ty);
            return quote! { #at: #d };
        }
        let j = &f.json;
        let r = read_of(&f.ty, quote!(v.get(#j)));
        quote! { #at: #r }
    });

    quote! {
        impl ::zip::Wire for #name {
            fn describe() -> &'static ::zip::TypeDesc {
                static DESC: ::zip::TypeDesc = ::zip::TypeDesc {
                    id: #id,
                    text: false,
                    fields: &[#(#descs),*],
                };
                &DESC
            }
            fn write_json(&self, out: &mut ::std::string::String) {
                out.push('{');
                #(#writes)*
                out.push('}');
            }
            fn read_json(v: &::zip::Json) -> ::std::result::Result<Self, ::zip::Error> {
                Ok(#name { #(#reads),* })
            }
        }
    }
}

/// newtype is the impl for a value type. `text` carries it as one word, which
/// is what a quoted decimal is and what a URL can hold.
fn newtype(name: &syn::Ident, id: &str, a: &Attrs, inner: &Type) -> TokenStream {
    if a.text {
        return quote! {
            impl ::zip::Wire for #name {
                fn describe() -> &'static ::zip::TypeDesc {
                    static DESC: ::zip::TypeDesc = ::zip::TypeDesc { id: #id, text: true, fields: &[] };
                    &DESC
                }
                fn write_json(&self, out: &mut ::std::string::String) {
                    ::zip::json::write_str(out, &::std::string::ToString::to_string(&self.0));
                }
                fn read_json(v: &::zip::Json) -> ::std::result::Result<Self, ::zip::Error> {
                    let s = v.text()?;
                    match ::std::str::FromStr::from_str(s) {
                        Ok(n) => Ok(#name(n)),
                        Err(_) => Err(::zip::Error::bad(concat!("not a ", #id, ": "))),
                    }
                }
            }
        };
    }
    let w = write_of(inner, quote!(&self.0));
    let r = read_of(inner, quote!(::std::option::Option::Some(v)));
    quote! {
        impl ::zip::Wire for #name {
            fn describe() -> &'static ::zip::TypeDesc {
                static DESC: ::zip::TypeDesc = ::zip::TypeDesc { id: #id, text: false, fields: &[] };
                &DESC
            }
            fn write_json(&self, out: &mut ::std::string::String) { #w; }
            fn read_json(v: &::zip::Json) -> ::std::result::Result<Self, ::zip::Error> {
                Ok(#name(#r))
            }
        }
    }
}

/// scalar_of is what a URL can put in this field, which is what lets a query
/// value become the JSON the one decoder reads.
fn scalar_of(t: &Type) -> TokenStream {
    match shape_of(t) {
        Shape::Prim(p) => match p {
            "string" => quote!(::zip::Scalar::Text),
            "bool" => quote!(::zip::Scalar::Bool),
            "f32" | "f64" => quote!(::zip::Scalar::Number),
            "bytes" => quote!(::zip::Scalar::Text),
            _ => quote!(::zip::Scalar::Number),
        },
        Shape::Opt(inner) => scalar_of(&inner),
        Shape::List(inner) => {
            let e = scalar_of(&inner);
            quote!(::zip::Scalar::List(&#e))
        }
        Shape::Named => quote!(::zip::Scalar::Named),
        _ => quote!(::zip::Scalar::Body),
    }
}

/// Shape is the structure of a written type, which is all a macro can see.
enum Shape {
    Prim(&'static str),
    Opt(Type),
    List(Type),
    Map(Type),
    Named,
    Other,
}

fn shape_of(t: &Type) -> Shape {
    match t {
        Type::Reference(r) => shape_of(&r.elem),
        Type::Paren(p) => shape_of(&p.elem),
        Type::Group(g) => shape_of(&g.elem),
        Type::Path(p) => {
            let Some(last) = p.path.segments.last() else {
                return Shape::Other;
            };
            let name = last.ident.to_string();
            let args: Vec<Type> = match &last.arguments {
                syn::PathArguments::AngleBracketed(b) => b
                    .args
                    .iter()
                    .filter_map(|g| match g {
                        syn::GenericArgument::Type(t) => Some(t.clone()),
                        _ => None,
                    })
                    .collect(),
                _ => vec![],
            };
            match (name.as_str(), args.len()) {
                ("Option", 1) => Shape::Opt(args[0].clone()),
                ("Vec", 1) | ("VecDeque", 1) => {
                    if ty::prim(&args[0]) == Some("u8") {
                        Shape::Prim("bytes")
                    } else {
                        Shape::List(args[0].clone())
                    }
                }
                ("HashMap", 2) | ("BTreeMap", 2) => Shape::Map(args[1].clone()),
                _ => match ty::prim(t) {
                    Some(p) => Shape::Prim(p),
                    None => Shape::Named,
                },
            }
        }
        _ => Shape::Other,
    }
}

/// write_of is the JSON one value writes.
///
/// Every case binds the value to a name first. A generated expression is
/// TOKENS, and `&self.chains` followed by `.iter()` is `&(self.chains.iter())`
/// — a reference to an iterator, which is not one. A binding has no precedence
/// to get wrong.
fn write_of(t: &Type, at: TokenStream) -> TokenStream {
    match shape_of(t) {
        Shape::Prim("string") => quote!({ let v = #at; ::zip::json::write_str(out, v); }),
        Shape::Prim("bool") => {
            quote!({ let v = #at; out.push_str(if *v { "true" } else { "false" }); })
        }
        Shape::Prim("bytes") => quote!({ let v = #at; ::zip::json::write_bytes(out, v); }),
        Shape::Prim("f32") | Shape::Prim("f64") => {
            quote!({ let v = #at; ::zip::json::write_float(out, *v as f64); })
        }
        Shape::Prim(_) => {
            quote!({ let v = #at; out.push_str(&::std::string::ToString::to_string(v)); })
        }
        Shape::Opt(inner) => {
            let w = write_of(&inner, quote!(it));
            quote! {
                {
                    let v = #at;
                    match v {
                        ::std::option::Option::Some(it) => { #w; }
                        ::std::option::Option::None => out.push_str("null"),
                    }
                }
            }
        }
        Shape::List(inner) => {
            let w = write_of(&inner, quote!(it));
            quote! {
                {
                    let v = #at;
                    out.push('[');
                    for (i, it) in v.iter().enumerate() {
                        if i > 0 { out.push(','); }
                        #w;
                    }
                    out.push(']');
                }
            }
        }
        Shape::Map(inner) => {
            let w = write_of(&inner, quote!(it));
            quote! {
                {
                    let v = #at;
                    // Sorted, because a map has no order and a document that
                    // changes shape between two identical answers is one nothing
                    // can diff.
                    let mut keys: ::std::vec::Vec<_> = v.keys().collect();
                    keys.sort();
                    out.push('{');
                    for (i, k) in keys.into_iter().enumerate() {
                        if i > 0 { out.push(','); }
                        ::zip::json::write_str(out, k);
                        out.push(':');
                        let it = &v[k];
                        #w;
                    }
                    out.push('}');
                }
            }
        }
        _ => quote!({ let v = #at; ::zip::Wire::write_json(v, out); }),
    }
}

/// read_of is one value, out of the JSON that arrived. `from` is an
/// Option<&Json>: a field the document did not carry reads as its zero, which
/// is what a decoder does everywhere else.
fn read_of(t: &Type, from: TokenStream) -> TokenStream {
    match shape_of(t) {
        Shape::Prim("string") => quote!(::zip::json::as_text(#from)?),
        Shape::Prim("bool") => quote!(::zip::json::as_bool(#from)?),
        Shape::Prim("bytes") => quote!(::zip::json::as_bytes(#from)?),
        Shape::Prim(p) => {
            let cast = format_ident!("{}", num_of(p));
            quote!(::zip::json::as_number(#from)? as #cast)
        }
        Shape::Opt(inner) => {
            let r = read_of(&inner, quote!(::std::option::Option::Some(one)));
            quote! {
                match #from {
                    ::std::option::Option::Some(one) if !one.is_null() => ::std::option::Option::Some(#r),
                    _ => ::std::option::Option::None,
                }
            }
        }
        Shape::List(inner) => {
            let r = read_of(&inner, quote!(::std::option::Option::Some(one)));
            quote! {
                {
                    let mut got = ::std::vec::Vec::new();
                    for one in ::zip::json::as_list(#from)? { got.push(#r); }
                    got
                }
            }
        }
        Shape::Map(inner) => {
            let r = read_of(&inner, quote!(::std::option::Option::Some(one)));
            quote! {
                {
                    let mut got = ::std::collections::HashMap::new();
                    for (k, one) in ::zip::json::as_map(#from)? { got.insert(k.clone(), #r); }
                    got
                }
            }
        }
        _ => quote!(::zip::json::as_wire(#from)?),
    }
}

/// default_of is the zero a field the wire does not carry starts at.
fn default_of(t: &Type) -> TokenStream {
    match shape_of(t) {
        Shape::Opt(_) => quote!(::std::option::Option::None),
        _ => quote!(::std::default::Default::default()),
    }
}

fn num_of(p: &str) -> &'static str {
    match p {
        "i8" => "i8",
        "i16" => "i16",
        "i32" => "i32",
        "i64" => "i64",
        "u8" => "u8",
        "u16" => "u16",
        "u32" => "u32",
        "f32" => "f32",
        "f64" => "f64",
        _ => "u64",
    }
}
