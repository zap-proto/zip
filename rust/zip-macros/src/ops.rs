// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! `#[zip::ops]` — a service's operations, declared where they are written.
//!
//! An impl block is the registry. A method carrying `#[get("/path")]` is an op:
//! its name, its address, the type it takes, the type it answers with and the
//! sentence above it are all on the same item, so one pass reads the whole
//! declaration. Nothing is registered twice and nothing is written twice.
//!
//! What comes out is the App a `main` serves and, as a side effect of
//! compiling, the fragment of the manifest that describes these ops. The op is
//! not given a name here: [zip.ID] settles that, once, for every language, so a
//! Rust op and a Go op at the same address carry the same token.

use proc_macro2::TokenStream;
use quote::quote;
use syn::{FnArg, ImplItem, ItemImpl, ReturnType, Type};

use crate::{doc, frag};

const VERBS: [&str; 5] = ["get", "post", "put", "patch", "delete"];

pub fn expand(args: TokenStream, mut block: ItemImpl) -> Result<TokenStream, syn::Error> {
    let about = About::parse(args)?;
    let holder = match &*block.self_ty {
        Type::Path(p) => p
            .path
            .segments
            .last()
            .map(|s| s.ident.clone())
            .ok_or_else(|| syn::Error::new_spanned(&block.self_ty, "zip::ops: expected a type"))?,
        other => return Err(syn::Error::new_spanned(other, "zip::ops: expected a type")),
    };

    let mut ops = Vec::new();
    for item in &mut block.items {
        let ImplItem::Fn(f) = item else { continue };
        let Some((verb, path)) = route(&f.attrs)? else {
            continue;
        };
        ops.push(Op::read(verb, path, f)?);
        f.attrs = doc::keep(&f.attrs, &VERBS);
    }
    if ops.is_empty() {
        return Err(syn::Error::new_spanned(
            &block.self_ty,
            "zip::ops: no method carries a route; an impl block with no op declares nothing",
        ));
    }

    frag::write("ops", &holder.to_string(), &fragment(&about, &ops));

    let app = about.app;
    let title = about.title;
    let version = about.version;
    let describe = about.describe;
    let registrations = ops.iter().map(Op::register);
    let expanded = quote! {
        #block

        impl #holder {
            /// Ops is this service's typed operations: the REST routes, the ZAP
            /// methods, and the document that describes them, from the one
            /// registration each op already carries.
            pub fn ops(self) -> ::zip::App {
                let holder = ::std::sync::Arc::new(self);
                let mut app = ::zip::App::new(#app, #title, #version, #describe);
                #(#registrations)*
                app
            }
        }
    };
    Ok(expanded)
}

/// About is what the app says about itself: the facts a document's info block
/// carries and no handler knows.
struct About {
    app: String,
    title: String,
    version: String,
    describe: String,
}

impl About {
    fn parse(args: TokenStream) -> Result<Self, syn::Error> {
        let mut out = About {
            app: String::new(),
            title: String::new(),
            version: String::new(),
            describe: String::new(),
        };
        if args.is_empty() {
            return Ok(out);
        }
        let items = syn::parse::Parser::parse2(
            syn::punctuated::Punctuated::<syn::Meta, syn::Token![,]>::parse_terminated,
            args,
        )?;
        for item in items {
            let syn::Meta::NameValue(nv) = &item else {
                return Err(syn::Error::new_spanned(
                    &item,
                    "zip::ops: known keys are app, title, version, description",
                ));
            };
            let syn::Expr::Lit(l) = &nv.value else {
                return Err(syn::Error::new_spanned(
                    &nv.value,
                    "zip::ops: expected a string",
                ));
            };
            let syn::Lit::Str(s) = &l.lit else {
                return Err(syn::Error::new_spanned(
                    &nv.value,
                    "zip::ops: expected a string",
                ));
            };
            let v = s.value();
            if nv.path.is_ident("app") {
                out.app = v;
            } else if nv.path.is_ident("title") {
                out.title = v;
            } else if nv.path.is_ident("version") {
                out.version = v;
            } else if nv.path.is_ident("description") {
                out.describe = v;
            } else {
                return Err(syn::Error::new_spanned(
                    &nv.path,
                    "zip::ops: known keys are app, title, version, description",
                ));
            }
        }
        Ok(out)
    }
}

/// Op is one declared operation.
struct Op {
    verb: String,
    path: String,
    call: syn::Ident,
    input: Option<Type>,
    output: Option<Type>,
    doc: doc::Doc,
}

impl Op {
    fn read(verb: String, path: String, f: &syn::ImplItemFn) -> Result<Self, syn::Error> {
        let mut input = None;
        for arg in f.sig.inputs.iter() {
            match arg {
                FnArg::Receiver(_) => {}
                FnArg::Typed(t) => {
                    if input.is_some() {
                        return Err(syn::Error::new_spanned(
                            arg,
                            "zip::ops: an op takes one input; a request is one value",
                        ));
                    }
                    input = Some(strip(&t.ty));
                }
            }
        }
        let output = match &f.sig.output {
            ReturnType::Default => None,
            ReturnType::Type(_, t) => answer(t),
        };
        Ok(Op {
            verb,
            path,
            call: f.sig.ident.clone(),
            input,
            output,
            doc: doc::read(&f.attrs),
        })
    }

    /// register is the line that puts this op on the app: decode the input the
    /// way the document says it arrives, run the handler, write the answer.
    fn register(&self) -> TokenStream {
        let verb = self.verb.to_uppercase();
        let path = &self.path;
        let call = &self.call;
        let run = match (&self.input, &self.output) {
            (Some(i), Some(_)) => quote! {
                let arg = ::zip::bind::<#i>(input)?;
                let answer = holder.#call(&arg)?;
                let mut out = ::std::string::String::new();
                ::zip::Wire::write_json(&answer, &mut out);
                Ok(::std::option::Option::Some(out))
            },
            (Some(i), None) => quote! {
                let arg = ::zip::bind::<#i>(input)?;
                holder.#call(&arg)?;
                Ok(::std::option::Option::None)
            },
            (None, Some(_)) => quote! {
                let answer = holder.#call()?;
                let mut out = ::std::string::String::new();
                ::zip::Wire::write_json(&answer, &mut out);
                Ok(::std::option::Option::Some(out))
            },
            (None, None) => quote! {
                holder.#call()?;
                Ok(::std::option::Option::None)
            },
        };
        let describe = match &self.input {
            Some(i) => quote!(::std::option::Option::Some(<#i as ::zip::Wire>::describe())),
            None => quote!(::std::option::Option::None),
        };
        quote! {
            app.op(#verb, #path, #describe, {
                let holder = ::std::sync::Arc::clone(&holder);
                ::std::boxed::Box::new(move |input: &::zip::Input| { #run })
            });
        }
    }

    fn json(&self) -> String {
        let mut o = frag::Object::new();
        o.text("method", &self.verb.to_uppercase())
            .text("path", &self.path)
            .text("description", &self.doc.text)
            .text("pkg", &std::env::var("CARGO_PKG_NAME").unwrap_or_default());
        if let Some(t) = &self.input {
            o.text("in", &name_of(t));
        }
        if let Some(t) = &self.output {
            o.text("out", &name_of(t));
        }
        if !self.doc.example.is_empty() {
            o.raw("example", &self.doc.example);
        }
        if !self.doc.response.is_empty() {
            o.raw("response", &self.doc.response);
        }
        o.finish()
    }
}

fn fragment(about: &About, ops: &[Op]) -> String {
    let items: Vec<String> = ops.iter().map(Op::json).collect();
    let mut o = frag::Object::new();
    o.number("manifest", 1)
        .text("app", &about.app)
        .text("title", &about.title)
        .text("version", &about.version)
        .text("description", &about.describe)
        .raw("ops", &frag::list(&items));
    o.finish()
}

/// route reads `#[get("/path")]` and its siblings off a method.
fn route(attrs: &[syn::Attribute]) -> Result<Option<(String, String)>, syn::Error> {
    for a in attrs {
        for verb in VERBS {
            if !a.path().is_ident(verb) {
                continue;
            }
            let lit: syn::LitStr = a.parse_args().map_err(|_| {
                syn::Error::new_spanned(
                    a,
                    "zip::ops: a route is a string literal — an op with a computed path has no identity to document",
                )
            })?;
            return Ok(Some((verb.to_string(), lit.value())));
        }
    }
    Ok(None)
}

/// strip removes the reference an argument is passed by: what a handler reads
/// is the value, and how it borrows it is Rust's business, not the wire's.
fn strip(t: &Type) -> Type {
    match t {
        Type::Reference(r) => strip(&r.elem),
        Type::Paren(p) => strip(&p.elem),
        Type::Group(g) => strip(&g.elem),
        other => other.clone(),
    }
}

/// answer is the type inside `Result<T, _>`, or None for an op that answers
/// nothing.
fn answer(t: &Type) -> Option<Type> {
    let Type::Path(p) = t else { return None };
    let last = p.path.segments.last()?;
    if last.ident != "Result" {
        return None;
    }
    let syn::PathArguments::AngleBracketed(b) = &last.arguments else {
        return None;
    };
    for g in &b.args {
        if let syn::GenericArgument::Type(inner) = g {
            if is_unit(inner) {
                return None;
            }
            return Some(strip(inner));
        }
    }
    None
}

fn is_unit(t: &Type) -> bool {
    matches!(t, Type::Tuple(t) if t.elems.is_empty())
}

/// name_of is the type's own name — all a macro can see of it, and all the
/// manifest needs, because the type describes itself where it is declared.
fn name_of(t: &Type) -> String {
    match t {
        Type::Path(p) => p
            .path
            .segments
            .last()
            .map(|s| s.ident.to_string())
            .unwrap_or_default(),
        _ => String::new(),
    }
}
