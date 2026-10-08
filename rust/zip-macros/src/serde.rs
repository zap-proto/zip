// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! What serde was told about a type's names.
//!
//! The derive reads `#[serde(...)]` for the four things that decide what a
//! field is called on the wire — `rename`, `rename_all`, `skip` and `flatten` —
//! plus `with`, which is how a byte field says it is base64. Everything else
//! serde accepts is passed over: it changes how a value is read, not what it is
//! called.

use proc_macro2::TokenStream;
use syn::meta::ParseNestedMeta;
use syn::{Attribute, LitStr, Token};

/// Container is what a struct told serde.
#[derive(Default)]
pub struct Container {
    pub rename_all: Option<String>,
    pub transparent: bool,
}

impl Container {
    pub fn read(attrs: &[Attribute]) -> Result<Self, syn::Error> {
        let mut out = Container::default();
        for a in serde(attrs) {
            a.parse_nested_meta(|meta| {
                if meta.path.is_ident("rename_all") {
                    let rule = both_ways(&meta)?;
                    if !RULES.contains(&rule.as_str()) {
                        return Err(meta.error(format!("zip::Wire: no rename_all rule {rule:?}")));
                    }
                    out.rename_all = Some(rule);
                } else if meta.path.is_ident("transparent") {
                    out.transparent = true;
                } else {
                    pass(&meta)?;
                }
                Ok(())
            })?;
        }
        Ok(out)
    }
}

/// Field is what a field told serde.
#[derive(Default)]
pub struct Field {
    pub rename: Option<String>,
    pub skip: bool,
    pub flatten: bool,
    pub with: Option<String>,
}

impl Field {
    pub fn read(attrs: &[Attribute]) -> Result<Self, syn::Error> {
        let mut out = Field::default();
        let (mut ser, mut de) = (false, false);
        for a in serde(attrs) {
            a.parse_nested_meta(|meta| {
                if meta.path.is_ident("rename") {
                    out.rename = Some(both_ways(&meta)?);
                } else if meta.path.is_ident("skip") {
                    out.skip = true;
                } else if meta.path.is_ident("skip_serializing") {
                    ser = true;
                } else if meta.path.is_ident("skip_deserializing") {
                    de = true;
                } else if meta.path.is_ident("flatten") {
                    out.flatten = true;
                } else if meta.path.is_ident("with") {
                    out.with = Some(meta.value()?.parse::<LitStr>()?.value());
                } else {
                    pass(&meta)?;
                }
                Ok(())
            })?;
        }
        out.skip |= ser && de;
        Ok(out)
    }
}

fn serde(attrs: &[Attribute]) -> impl Iterator<Item = &Attribute> {
    attrs.iter().filter(|a| a.path().is_ident("serde"))
}

/// both_ways reads `name = "x"`, or `name(serialize = "x", deserialize = "x")`
/// when the two agree. A name that differs by direction is two names, and a
/// document can publish one.
fn both_ways(meta: &ParseNestedMeta) -> Result<String, syn::Error> {
    if meta.input.peek(Token![=]) {
        return Ok(meta.value()?.parse::<LitStr>()?.value());
    }
    let (mut ser, mut de) = (None, None);
    meta.parse_nested_meta(|m| {
        let v = m.value()?.parse::<LitStr>()?.value();
        if m.path.is_ident("serialize") {
            ser = Some(v);
        } else if m.path.is_ident("deserialize") {
            de = Some(v);
        } else {
            return Err(m.error("expected serialize or deserialize"));
        }
        Ok(())
    })?;
    match (ser, de) {
        (Some(a), Some(b)) if a == b => Ok(a),
        _ => Err(meta.error(
            "zip::Wire: a name is the same both ways; one that differs by direction is two names, and a document publishes one",
        )),
    }
}

/// pass consumes an attribute this derive does not read, whatever its shape.
fn pass(meta: &ParseNestedMeta) -> Result<(), syn::Error> {
    if meta.input.peek(Token![=]) {
        meta.value()?.parse::<syn::Expr>()?;
    } else if meta.input.peek(syn::token::Paren) {
        let inner;
        syn::parenthesized!(inner in meta.input);
        inner.parse::<TokenStream>()?;
    }
    Ok(())
}

/// RULES are serde's rename_all rules.
const RULES: [&str; 8] = [
    "lowercase",
    "UPPERCASE",
    "PascalCase",
    "camelCase",
    "snake_case",
    "SCREAMING_SNAKE_CASE",
    "kebab-case",
    "SCREAMING-KEBAB-CASE",
];

/// case is a field's name under a rename_all rule, as serde spells it
/// (serde_derive's `RenameRule::apply_to_field`).
pub fn case(rule: &Option<String>, field: &str) -> String {
    let Some(rule) = rule else {
        return field.to_string();
    };
    match rule.as_str() {
        "UPPERCASE" | "SCREAMING_SNAKE_CASE" => field.to_ascii_uppercase(),
        "PascalCase" => pascal(field),
        "camelCase" => {
            let p = pascal(field);
            let mut c = p.chars();
            match c.next() {
                Some(first) => first.to_ascii_lowercase().to_string() + c.as_str(),
                None => p,
            }
        }
        "kebab-case" => field.replace('_', "-"),
        "SCREAMING-KEBAB-CASE" => field.to_ascii_uppercase().replace('_', "-"),
        _ => field.to_string(), // lowercase and snake_case leave a field as it is.
    }
}

fn pascal(field: &str) -> String {
    let mut out = String::with_capacity(field.len());
    let mut up = true;
    for ch in field.chars() {
        if ch == '_' {
            up = true;
        } else if up {
            out.push(ch.to_ascii_uppercase());
            up = false;
        } else {
            out.push(ch);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rules_spell_as_serde_does() {
        let r = |s: &str| Some(s.to_string());
        assert_eq!(case(&None, "node_id"), "node_id");
        assert_eq!(case(&r("camelCase"), "node_id"), "nodeId");
        assert_eq!(case(&r("camelCase"), "max_tokens"), "maxTokens");
        assert_eq!(case(&r("PascalCase"), "node_id"), "NodeId");
        assert_eq!(case(&r("SCREAMING_SNAKE_CASE"), "node_id"), "NODE_ID");
        assert_eq!(case(&r("kebab-case"), "node_id"), "node-id");
        assert_eq!(case(&r("SCREAMING-KEBAB-CASE"), "node_id"), "NODE-ID");
        assert_eq!(case(&r("lowercase"), "node_id"), "node_id");
        assert_eq!(case(&r("snake_case"), "node_id"), "node_id");
    }
}
