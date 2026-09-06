// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! A Rust type, said neutrally.
//!
//! The macro sees the type as it is WRITTEN — `Vec<Peer>`, `Option<String>`,
//! `HashMap<String, Lp>` — and not what it resolves to, because a macro is
//! syntax and nothing more. That is enough: the shape of a container is in its
//! spelling, and a name that is not a container is a reference to a type that
//! describes itself.
//!
//! It is also the limit the study named. A field whose type comes from another
//! crate is a NAME here and nothing else; what that name means arrives from the
//! type's own description, which travels with the type.

use syn::{GenericArgument, PathArguments, Type};

/// Ref is a field's type as a manifest TypeRef.
pub struct Ref {
    /// json is the rendered TypeRef object.
    pub json: String,
    /// spell is the type as the source writes it, for the ledger.
    pub spell: String,
}

/// of reads one type.
pub fn of(t: &Type) -> Ref {
    Ref {
        json: body(t, false),
        spell: crate::doc::spell(t),
    }
}

fn body(t: &Type, opt: bool) -> String {
    match t {
        Type::Reference(r) => body(&r.elem, opt),
        Type::Paren(p) => body(&p.elem, opt),
        Type::Group(g) => body(&g.elem, opt),
        Type::Slice(s) => list(&s.elem, 0, opt),
        Type::Array(a) => {
            let n = len(&a.len);
            if prim(&a.elem) == Some("u8") {
                return wrap(&format!("\"prim\":\"bytes_fixed[{n}]\""), opt);
            }
            list(&a.elem, n, opt)
        }
        Type::Path(p) => {
            let last = match p.path.segments.last() {
                Some(s) => s,
                None => return wrap("\"prim\":\"any\"", opt),
            };
            let name = last.ident.to_string();
            let args = generics(&last.arguments);
            match (name.as_str(), args.len()) {
                ("Option", 1) => body(args[0], true),
                ("Box", 1) | ("Arc", 1) | ("Rc", 1) | ("Cow", 1) => body(args[0], opt),
                ("Vec", 1) | ("VecDeque", 1) => {
                    if prim(args[0]) == Some("u8") {
                        return wrap("\"prim\":\"bytes\"", opt);
                    }
                    list(args[0], 0, opt)
                }
                ("HashMap", 2) | ("BTreeMap", 2) => {
                    wrap(&format!("\"map\":{}", body(args[1], false)), opt)
                }
                _ => match prim(t) {
                    Some(p) => wrap(&format!("\"prim\":\"{p}\""), opt),
                    None => wrap(&format!("\"ref\":{}", crate::frag::quote(&name)), opt),
                },
            }
        }
        _ => wrap("\"prim\":\"any\"", opt),
    }
}

fn list(elem: &Type, n: usize, opt: bool) -> String {
    let mut fields = format!("\"list\":{}", body(elem, false));
    if n > 0 {
        fields.push_str(&format!(",\"len\":{n}"));
    }
    wrap(&fields, opt)
}

fn wrap(fields: &str, opt: bool) -> String {
    if opt {
        format!("{{{fields},\"opt\":true}}")
    } else {
        format!("{{{fields}}}")
    }
}

/// prim is the neutral spelling of a built-in, or None for a named type.
pub fn prim(t: &Type) -> Option<&'static str> {
    let name = match t {
        Type::Reference(r) => return prim(&r.elem),
        Type::Path(p) => p.path.segments.last()?.ident.to_string(),
        _ => return None,
    };
    Some(match name.as_str() {
        "bool" => "bool",
        "i8" => "i8",
        "i16" => "i16",
        "i32" => "i32",
        "i64" | "isize" => "i64",
        "u8" => "u8",
        "u16" => "u16",
        "u32" => "u32",
        "u64" | "usize" => "u64",
        "f32" => "f32",
        "f64" => "f64",
        "String" | "str" => "string",
        _ => return None,
    })
}

fn generics(a: &PathArguments) -> Vec<&Type> {
    match a {
        PathArguments::AngleBracketed(b) => b
            .args
            .iter()
            .filter_map(|g| match g {
                GenericArgument::Type(t) => Some(t),
                _ => None,
            })
            .collect(),
        _ => vec![],
    }
}

fn len(e: &syn::Expr) -> usize {
    if let syn::Expr::Lit(l) = e {
        if let syn::Lit::Int(i) = &l.lit {
            return i.base10_parse().unwrap_or(0);
        }
    }
    0
}
