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
//! The bytes are serde's. A field's wire name is the one serde reads it under —
//! its `#[serde(rename)]`, else the struct's `rename_all` applied to it, else
//! its own name — so the derive reads those attributes rather than asking for
//! the name a second time, and what the document says a field is called is what
//! the decoder reads it under. What serde cannot be told, and so this cannot
//! describe — a flattened field, a name that differs by direction — is refused
//! at compile time rather than published wrong.

use proc_macro2::TokenStream;
use quote::quote;
use syn::ext::IdentExt;
use syn::{Data, DeriveInput, Fields, Type};

use crate::attr::Attrs;
use crate::serde::{self as sd, Container};
use crate::{doc, frag, ty};

pub fn derive(input: DeriveInput) -> Result<TokenStream, syn::Error> {
    let name = input.ident.clone();
    let id = name.to_string();
    let own = Attrs::read(&input.attrs)?;
    let prose = doc::read(&input.attrs);
    let container = Container::read(&input.attrs)?;

    let Data::Struct(s) = &input.data else {
        return Err(syn::Error::new_spanned(
            &input.ident,
            "zip::Wire describes a struct: a value with fields, or a newtype that is one value",
        ));
    };

    match &s.fields {
        Fields::Named(named) => {
            if container.transparent {
                return Err(syn::Error::new_spanned(
                    &input.ident,
                    "zip::Wire: a transparent struct is one value; write it as a newtype",
                ));
            }
            let fields: Vec<Field> = named
                .named
                .iter()
                .map(|f| Field::read(f, &container))
                .collect::<Result<_, _>>()?;
            Ok(shape(
                &name,
                &id,
                &record(&id, &prose.text, &fields),
                &fields,
            ))
        }
        Fields::Unnamed(un) if un.unnamed.len() == 1 => {
            let inner = &un.unnamed[0].ty;
            Ok(newtype(&name, &id, &value(&id, &own, inner), &own))
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
    fn read(f: &syn::Field, container: &Container) -> Result<Self, syn::Error> {
        let a = Attrs::read(&f.attrs)?;
        let name = f.ident.clone().expect("named field");
        if a.json.is_some() {
            return Err(syn::Error::new_spanned(
                f,
                "zip::Wire: a field's wire name is serde's — #[serde(rename = \"...\")]",
            ));
        }
        let serde = sd::Field::read(&f.attrs)?;
        if serde.flatten {
            return Err(syn::Error::new_spanned(
                f,
                "zip::Wire: a flattened field has no name of its own to describe; name the struct it holds as a field",
            ));
        }
        bytes(f, &f.ty, serde.with.as_deref())?;
        let json = if serde.skip {
            "-".to_string()
        } else if let Some(n) = serde.rename {
            n
        } else {
            sd::case(&container.rename_all, &name.unraw().to_string())
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

/// bytes refuses a byte field serde would write as an array of numbers. The
/// document says a byte field is base64 in a string, which is what Go writes,
/// so a `Vec<u8>` carries `#[serde(with = "zip::base64")]`, and one inside a
/// container — which that attribute cannot reach — is refused.
fn bytes(f: &syn::Field, t: &Type, with: Option<&str>) -> Result<(), syn::Error> {
    if let Shape::Prim("bytes") = shape_of(t) {
        if with == Some("zip::base64") {
            return Ok(());
        }
        return Err(syn::Error::new_spanned(
            f,
            "zip::Wire: a byte field rides JSON as base64 — #[serde(with = \"zip::base64\")]",
        ));
    }
    if holds_bytes(t) {
        return Err(syn::Error::new_spanned(
            f,
            "zip::Wire: bytes inside a container have no base64 spelling here; make them a field of their own type",
        ));
    }
    Ok(())
}

fn holds_bytes(t: &Type) -> bool {
    match shape_of(t) {
        Shape::Prim("bytes") => true,
        Shape::Opt(inner) | Shape::List(inner) | Shape::Map(inner) => holds_bytes(&inner),
        _ => false,
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

/// shape is the impl for a struct: the description a binder reads and the
/// entry a manifest carries.
fn shape(name: &syn::Ident, id: &str, stated: &str, fields: &[Field]) -> TokenStream {
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

    let reaches = fields
        .iter()
        .filter_map(|f| named_in(&f.ty))
        .map(|path| quote!(<#path as ::zip::Wire>::reach(into);));

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
            fn stated() -> &'static str { #stated }
            fn reach(into: &mut ::std::vec::Vec<(&'static str, &'static str)>) {
                if into.iter().any(|(k, _)| *k == #id) {
                    return; // claimed before the fields are walked: the cycle guard.
                }
                into.push((#id, #stated));
                #(#reaches)*
            }
        }
    }
}

/// newtype is the impl for a value type. A `text` one is carried as one word —
/// what a quoted decimal is, and what a URL can hold — so its serde impls are
/// written here, from its Display and FromStr: that is what `text` means, and
/// serde has no derive that says it. Any other newtype is serde's, which
/// carries a newtype as the value it holds.
fn newtype(name: &syn::Ident, id: &str, stated: &str, a: &Attrs) -> TokenStream {
    let a_text = a.text;
    let wire = quote! {
        impl ::zip::Wire for #name {
            fn describe() -> &'static ::zip::TypeDesc {
                static DESC: ::zip::TypeDesc = ::zip::TypeDesc { id: #id, text: #a_text, fields: &[] };
                &DESC
            }
            fn stated() -> &'static str { #stated }
            fn reach(into: &mut ::std::vec::Vec<(&'static str, &'static str)>) {
                if into.iter().all(|(k, _)| *k != #id) {
                    into.push((#id, #stated));
                }
            }
        }
    };
    if !a.text {
        return wire;
    }
    quote! {
        #wire
        impl ::zip::serde::Serialize for #name {
            fn serialize<S: ::zip::serde::Serializer>(&self, s: S) -> ::std::result::Result<S::Ok, S::Error> {
                s.collect_str(&self.0)
            }
        }
        impl<'de> ::zip::serde::Deserialize<'de> for #name {
            fn deserialize<D: ::zip::serde::Deserializer<'de>>(d: D) -> ::std::result::Result<Self, D::Error> {
                let text = <::std::string::String as ::zip::serde::Deserialize>::deserialize(d)?;
                match ::std::str::FromStr::from_str(&text) {
                    Ok(v) => Ok(#name(v)),
                    Err(_) => Err(<D::Error as ::zip::serde::de::Error>::custom(
                        ::std::format!("not a {}: {:?}", #id, text),
                    )),
                }
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
            "any" => quote!(::zip::Scalar::Body),
            _ => quote!(::zip::Scalar::Number),
        },
        Shape::Opt(inner) => scalar_of(&inner),
        Shape::List(inner) => {
            let e = scalar_of(&inner);
            quote!(::zip::Scalar::List(&#e))
        }
        Shape::Named(_) => quote!(::zip::Scalar::Named),
        _ => quote!(::zip::Scalar::Body),
    }
}

/// named_in is the path of the described type a field reaches, through whatever
/// container holds it, or None for a field made only of primitives.
fn named_in(t: &Type) -> Option<syn::Path> {
    match shape_of(t) {
        Shape::Named(p) => Some(p),
        Shape::Opt(inner) | Shape::List(inner) | Shape::Map(inner) => named_in(&inner),
        _ => None,
    }
}

/// Shape is the structure of a written type, which is all a macro can see.
enum Shape {
    Prim(&'static str),
    Opt(Type),
    List(Type),
    Map(Type),
    Named(syn::Path),
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
                ("Box", 1) | ("Arc", 1) | ("Rc", 1) => shape_of(&args[0]),
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
                    None => Shape::Named(p.path.clone()),
                },
            }
        }
        _ => Shape::Other,
    }
}
