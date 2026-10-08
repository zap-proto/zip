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

    let stated = fragment(&about, &ops);
    let reaches: Vec<TokenStream> = ops
        .iter()
        .flat_map(|o| [o.input.described(), o.output.described()])
        .flatten()
        .map(|t| quote!(<#t as ::zip::Wire>::reach(&mut types);))
        .collect();

    let app = about.app;
    let title = about.title;
    let version = about.version;
    let describe = about.describe;
    let registrations = ops.iter().map(Op::register);
    let expanded = quote! {
        #block

        impl #holder {
            /// Manifest is what this service declared, said neutrally: the ops
            /// the impl block carries and every type they reach.
            ///
            /// It is built from what the compiler read, so it cannot describe a
            /// service other than this one. `zipc` projects it into the OpenAPI
            /// document, the MCP tool list, the CLI and the ZAP schema.
            pub fn manifest() -> ::std::string::String {
                let mut types: ::std::vec::Vec<(&'static str, &'static str)> = ::std::vec::Vec::new();
                #(#reaches)*
                ::zip::manifest(#stated, &types)
            }

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
    /// args is the handler's parameters after `&self`, in the order declared.
    args: Vec<Arg>,
    input: In,
    output: Answer,
    doc: doc::Doc,
}

/// Arg is one parameter a handler takes.
enum Arg {
    /// Cx is `&zip::Cx`.
    Cx,
    /// Input is the op's input, by reference or by value.
    Input { by_ref: bool },
}

/// In is what an op reads its input from.
enum In {
    None,
    /// Value is a described type, bound from the body, the URL and headers.
    Value(Box<Type>),
    /// Body is the request body as it arrives, bound to nothing.
    Body,
}

/// Answer is what an op answers with.
enum Answer {
    None,
    /// Value is one described type, as JSON.
    Value(Box<Type>),
    Sse,
    Body,
}

impl In {
    fn described(&self) -> Option<Type> {
        match self {
            In::Value(t) => Some((**t).clone()),
            _ => None,
        }
    }
}

impl Answer {
    fn described(&self) -> Option<Type> {
        match self {
            Answer::Value(t) => Some((**t).clone()),
            _ => None,
        }
    }
}

impl Op {
    fn read(verb: String, path: String, f: &syn::ImplItemFn) -> Result<Self, syn::Error> {
        if f.sig.asyncness.is_none() {
            return Err(syn::Error::new_spanned(
                f.sig.fn_token,
                "zip::ops: an op is an async fn; it runs on the server's tasks",
            ));
        }
        let mut args = Vec::new();
        let mut input = In::None;
        for arg in f.sig.inputs.iter() {
            let FnArg::Typed(t) = arg else { continue };
            let by_ref = matches!(&*t.ty, Type::Reference(_));
            let ty = strip(&t.ty);
            match last(&ty).as_deref() {
                Some("Cx") => {
                    if !by_ref {
                        return Err(syn::Error::new_spanned(
                            arg,
                            "zip::ops: the exchange is borrowed: `cx: &zip::Cx`",
                        ));
                    }
                    args.push(Arg::Cx);
                    continue;
                }
                Some("Body") => {
                    if by_ref {
                        return Err(syn::Error::new_spanned(
                            arg,
                            "zip::ops: a body is read as it arrives, so it is taken by value: `body: zip::Body`",
                        ));
                    }
                    if !matches!(input, In::None) {
                        return Err(one_input(arg));
                    }
                    input = In::Body;
                }
                _ => {
                    if !matches!(input, In::None) {
                        return Err(one_input(arg));
                    }
                    input = In::Value(Box::new(ty));
                }
            }
            args.push(Arg::Input { by_ref });
        }
        let output = match &f.sig.output {
            ReturnType::Type(_, t) if last(t).as_deref() == Some("Result") => match answer(t) {
                None => Answer::None,
                Some(t) => match last(&t).as_deref() {
                    Some("Sse") => Answer::Sse,
                    Some("Body") => Answer::Body,
                    _ => Answer::Value(Box::new(t)),
                },
            },
            other => {
                return Err(syn::Error::new_spanned(
                    other,
                    "zip::ops: an op answers Result<T, zip::Error>; a refusal is part of every op's contract",
                ))
            }
        };
        Ok(Op {
            verb,
            path,
            call: f.sig.ident.clone(),
            args,
            input,
            output,
            doc: doc::read(&f.attrs),
        })
    }

    /// register is the line that puts this op on the app: read the input the
    /// way the document says it arrives, run the handler, answer.
    fn register(&self) -> TokenStream {
        let verb = self.verb.to_uppercase();
        let path = &self.path;
        let call = &self.call;
        let take = match &self.input {
            In::None => quote! { ::std::mem::drop(body); },
            In::Body => quote! { let arg = body; },
            In::Value(i) => quote! { let arg = ::zip::bind::<#i>(&cx, body).await?; },
        };
        let pass = self.args.iter().map(|a| match a {
            Arg::Cx => quote!(&cx),
            Arg::Input { by_ref: true } => quote!(&arg),
            Arg::Input { by_ref: false } => quote!(arg),
        });
        let run = quote!(holder.#call(#(#pass),*).await?);
        let reply = match &self.output {
            Answer::None => quote! { #run; Ok(::zip::Reply::Empty) },
            Answer::Value(_) => quote! { let answer = #run; ::zip::Reply::json(&answer) },
            Answer::Sse | Answer::Body => {
                quote! { let answer = #run; Ok(::zip::Reply::from(answer)) }
            }
        };
        quote! {
            app.op(#verb, #path, {
                let holder = ::std::sync::Arc::clone(&holder);
                move |cx: ::zip::Cx, body: ::zip::Body| {
                    let holder = ::std::sync::Arc::clone(&holder);
                    async move {
                        let _ = &cx;
                        #take
                        #reply
                    }
                }
            });
        }
    }

    fn json(&self) -> String {
        let mut o = frag::Object::new();
        o.text("method", &self.verb.to_uppercase())
            .text("path", &self.path)
            .text("description", &self.doc.text)
            .text("pkg", &std::env::var("CARGO_PKG_NAME").unwrap_or_default());
        if let Some(t) = self.input.described() {
            o.text("in", &name_of(&t));
        }
        if let Some(t) = self.output.described() {
            o.text("out", &name_of(&t));
        }
        if !self.doc.example.is_empty() {
            o.raw("example", &self.doc.example);
        }
        if !self.doc.response.is_empty() {
            o.raw("response", &self.doc.response);
        }
        match self.output {
            Answer::Sse => {
                o.text("stream", "sse");
            }
            Answer::Body => {
                o.text("stream", "bytes");
            }
            _ => {}
        }
        o.flag("raw", matches!(self.input, In::Body));
        o.finish()
    }
}

fn one_input(arg: &FnArg) -> syn::Error {
    syn::Error::new_spanned(
        arg,
        "zip::ops: an op takes one input; a request is one value",
    )
}

/// last is the last segment of a type's path: what the macro can see of it.
fn last(t: &Type) -> Option<String> {
    match t {
        Type::Path(p) => p.path.segments.last().map(|s| s.ident.to_string()),
        _ => None,
    }
}

/// fragment is the ops half of the manifest — the object's MEMBERS, without its
/// braces, because [zip::manifest] closes it around the types half.
fn fragment(about: &About, ops: &[Op]) -> String {
    let items: Vec<String> = ops.iter().map(Op::json).collect();
    let mut o = frag::Object::new();
    o.number("manifest", 1)
        .text("app", &about.app)
        .text("title", &about.title)
        .text("version", &about.version)
        .text("description", &about.describe)
        .raw("ops", &frag::list(&items));
    let whole = o.finish();
    whole[1..whole.len() - 1].to_string()
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
