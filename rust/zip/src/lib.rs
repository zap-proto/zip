// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! zip — a ZAP-native web framework, in Rust.
//!
//! A handler is a method with a doc comment and a route on it. Registering it
//! yields the REST route, the ZAP method, the OpenAPI document, the MCP tool,
//! the CLI command and the ZAP IDL, from that one registration and the sentence
//! above it. Nothing is declared twice.
//!
//! ```ignore
//! #[zip::ops(app = "info", title = "Lux node info", version = "1.0.0")]
//! impl Info {
//!     /// Bootstrapped reports whether a chain has finished bootstrapping on this node.
//!     ///
//!     /// Example: {"chain": "X"}
//!     /// Response: {"isBootstrapped": true}
//!     #[get("/chain/bootstrapped")]
//!     fn bootstrapped(&self, arg: &BootstrappedArgs) -> Result<Bootstrapped, zip::Error> {
//!         Ok(Bootstrapped { is_bootstrapped: true })
//!     }
//! }
//! ```
//!
//! # Where the documents come from
//!
//! The macro writes what it read — the ops, the types, the prose — as the crate
//! compiles. `zipc` projects that description into the OpenAPI document, the MCP
//! tool list, the CLI and the .zap schema, and `zapgen` compiles the schema into
//! the zero-copy accessors this crate's own wire is built from.
//!
//! The projector is one program for all three languages, which is the whole
//! point: an operation carries one id, one summary and one schema whether it was
//! written in Rust, in Go or in C++, because one piece of code derived all
//! three. What is native is DECLARING an op and RUNNING it, and both of those
//! are Rust here, all the way down. No Go runs in a service built with this.

mod app;
mod desc;
mod error;
pub mod json;

pub mod http;
pub mod zaphttp;

// zap is the ZAP wire and wire is the ZAP-HTTP frames, both generated from
// wire.zap by zapgen. They are the only wire code here, and none of it is
// written by hand.
include!(concat!(env!("OUT_DIR"), "/generated.rs"));

pub use app::{bind, Answer, App, Call, Input, Op, Shared};
pub use desc::{FieldDesc, Scalar, TypeDesc, Wire};
pub use error::Error;
pub use json::Json;

/// The two macros. `ops` declares a service; `Wire` makes a type say what it is.
pub use zip_macros::{ops, Wire};

/// listen serves an app on every address given, and does not return.
///
/// One verb, whatever the transport: an address decides which door it is, so a
/// binary states all of its listeners the same way and a deployment changes one
/// without touching the service.
///
/// ```text
/// info serve --zap :9653 --http :8000
/// ```
pub fn listen(app: App, http: Option<&str>, zap: Option<&str>) -> std::io::Result<()> {
    let app = std::sync::Arc::new(app);
    let mut doors = Vec::new();
    if let Some(addr) = zap {
        let app = std::sync::Arc::clone(&app);
        let addr = addr.to_string();
        doors.push(std::thread::spawn(move || zaphttp::listen(app, &addr)));
    }
    if let Some(addr) = http {
        let app = std::sync::Arc::clone(&app);
        let addr = addr.to_string();
        doors.push(std::thread::spawn(move || http::listen(app, &addr)));
    }
    for door in doors {
        match door.join() {
            Ok(r) => r?,
            Err(_) => return Err(std::io::Error::other("a door stopped")),
        }
    }
    Ok(())
}
