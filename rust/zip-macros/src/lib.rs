// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! Declaring a zip op, in Rust.
//!
//! Two macros and nothing else. `#[zip::ops]` on an impl block turns the
//! methods carrying a route into a service; `#[derive(zip::Wire)]` on a type
//! makes it say what it is. Both read the doc comments that are already there,
//! so an operation's prose has one home — the source — and reaches the
//! document, the tool list and the command line from it.

mod attr;
mod doc;
mod frag;
mod ops;
mod ty;
mod wire;

use proc_macro::TokenStream;

/// ops turns an impl block into a service.
#[proc_macro_attribute]
pub fn ops(args: TokenStream, item: TokenStream) -> TokenStream {
    let block = match syn::parse::<syn::ItemImpl>(item) {
        Ok(b) => b,
        Err(e) => return e.to_compile_error().into(),
    };
    match ops::expand(args.into(), block) {
        Ok(t) => t.into(),
        Err(e) => e.to_compile_error().into(),
    }
}

/// Wire makes a type describe itself, and carry itself over JSON.
#[proc_macro_derive(Wire, attributes(zip))]
pub fn derive_wire(item: TokenStream) -> TokenStream {
    let input = match syn::parse::<syn::DeriveInput>(item) {
        Ok(i) => i,
        Err(e) => return e.to_compile_error().into(),
    };
    match wire::derive(input) {
        Ok(t) => t.into(),
        Err(e) => e.to_compile_error().into(),
    }
}
