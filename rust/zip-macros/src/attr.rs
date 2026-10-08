// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! `#[zip(...)]` — what a declaration says about itself beyond its type.
//!
//! One attribute, one vocabulary, on a type and on a field alike. With serde's
//! own attributes it is the counterpart of a Go struct tag: serde says the name
//! the body carries a field under (`#[serde(rename)]`) and whether it carries
//! it at all (`#[serde(skip)]`); this says the rest — the name a URL carries it
//! under, the header it reads, and whether the handler refuses to run without
//! it.

use syn::{Attribute, Expr, Lit, Meta, Token};

#[derive(Default)]
pub struct Attrs {
    /// json is a field's wire name given here, which is refused: the name is
    /// serde's, and two places to write it are two names.
    pub json: Option<String>,
    /// url is the name a URL carries it under; "-" opts out.
    pub url: Option<String>,
    /// header is the request header it reads.
    pub header: Option<String>,
    /// required says the handler refuses to run without it.
    pub required: bool,
    /// text says this type is carried as one word, so a URL can hold it.
    pub text: bool,
    /// json_schema is the shape a type states for itself, raw.
    pub json_schema: Option<String>,
}

impl Attrs {
    pub fn read(attrs: &[Attribute]) -> Result<Self, syn::Error> {
        let mut out = Attrs::default();
        for a in attrs {
            if !a.path().is_ident("zip") {
                continue;
            }
            let items = a.parse_args_with(
                syn::punctuated::Punctuated::<Meta, Token![,]>::parse_terminated,
            )?;
            for item in items {
                match &item {
                    Meta::Path(p) if p.is_ident("required") => out.required = true,
                    Meta::Path(p) if p.is_ident("skip") => {
                        return Err(syn::Error::new_spanned(
                            p,
                            "zip: whether a field rides the wire is serde's — #[serde(skip)]",
                        ))
                    }
                    Meta::Path(p) if p.is_ident("text") => out.text = true,
                    Meta::NameValue(nv) => {
                        let v = literal(&nv.value)?;
                        if nv.path.is_ident("json") {
                            // On a type, `json` is the schema it states; on a
                            // field, the name the body carries it under. The two
                            // never meet: a field has no schema of its own and a
                            // type has no name on the wire.
                            if v.trim_start().starts_with('{') {
                                out.json_schema = Some(v);
                            } else {
                                out.json = Some(v);
                            }
                        } else if nv.path.is_ident("url") {
                            out.url = Some(v);
                        } else if nv.path.is_ident("header") {
                            out.header = Some(v);
                        } else {
                            return Err(syn::Error::new_spanned(
                                &nv.path,
                                "zip: known keys are json, url, header, required, text",
                            ));
                        }
                    }
                    other => {
                        return Err(syn::Error::new_spanned(
                            other,
                            "zip: known keys are json, url, header, required, text",
                        ))
                    }
                }
            }
        }
        Ok(out)
    }
}

impl Attrs {
    /// json is the schema a type stated for itself, if it stated one.
    pub fn schema(&self) -> Option<&String> {
        self.json_schema.as_ref()
    }
}

fn literal(e: &Expr) -> Result<String, syn::Error> {
    if let Expr::Lit(l) = e {
        if let Lit::Str(s) = &l.lit {
            return Ok(s.value());
        }
    }
    Err(syn::Error::new_spanned(e, "zip: expected a string"))
}
