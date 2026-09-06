// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The prose, from where it is already written.
//!
//! `///` desugars to `#[doc = "…"]` before a macro ever runs, so the comment
//! above a handler reaches the macro as an attribute on the same item as the
//! signature and the path. Rust therefore needs no second pass over the source
//! and no generated file: the sentence a person wrote is read in the one place
//! the op is declared.
//!
//! The two special lines are the ones a document is unusable without. `Example:`
//! is the request a reader can press "try it" on, `Response:` the answer they
//! should get. They are lines of the comment rather than an attribute for the
//! same reason the prose is: one mechanism, one place.

use quote::ToTokens;
use syn::{Attribute, Expr, Lit, Meta, Type};

/// Doc is what a comment said.
pub struct Doc {
    /// text is the comment with the Example and Response lines taken out.
    pub text: String,
    /// example and response are raw JSON, so they land in the document exactly
    /// as written and are wrong loudly rather than quietly if malformed.
    pub example: String,
    pub response: String,
}

/// read pulls the doc comment off an item's attributes.
pub fn read(attrs: &[Attribute]) -> Doc {
    let mut prose: Vec<String> = Vec::new();
    let mut example = String::new();
    let mut response = String::new();
    for line in lines(attrs) {
        let t = line.trim();
        if let Some(rest) = t.strip_prefix("Example:") {
            example = rest.trim().to_string();
            continue;
        }
        if let Some(rest) = t.strip_prefix("Response:") {
            response = rest.trim().to_string();
            continue;
        }
        prose.push(line);
    }
    while prose.last().map(|l| l.trim().is_empty()).unwrap_or(false) {
        prose.pop();
    }
    Doc {
        text: prose.join("\n").trim().to_string(),
        example,
        response,
    }
}

/// lines is every `#[doc]` line, with the one leading space rustdoc adds
/// removed — the comment as it was typed, not as it was encoded.
fn lines(attrs: &[Attribute]) -> Vec<String> {
    let mut out = Vec::new();
    for a in attrs {
        let Meta::NameValue(nv) = &a.meta else {
            continue;
        };
        if !nv.path.is_ident("doc") {
            continue;
        }
        let Expr::Lit(l) = &nv.value else { continue };
        let Lit::Str(s) = &l.lit else { continue };
        let v = s.value();
        out.push(v.strip_prefix(' ').unwrap_or(&v).to_string());
    }
    out
}

/// keep is every attribute that is not a doc comment and not one of ours, so
/// the item the macro re-emits is the item the author wrote.
pub fn keep(attrs: &[Attribute], ours: &[&str]) -> Vec<Attribute> {
    attrs
        .iter()
        .filter(|a| !ours.iter().any(|n| a.path().is_ident(n)))
        .cloned()
        .collect()
}

/// spell is a type as the source writes it, for the ledger a person reads when
/// they go to change the declaration.
pub fn spell(t: &Type) -> String {
    let s = t.to_token_stream().to_string();
    s.replace(" < ", "<")
        .replace(" > ", ">")
        .replace(" >", ">")
        .replace("< ", "<")
        .replace(" ,", ",")
        .replace(" ::", "::")
        .replace(":: ", "::")
        .replace("& ", "&")
        .replace(" ;", ";")
}
