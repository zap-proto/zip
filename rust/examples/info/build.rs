// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The documents this service publishes, if they have been projected yet.
//!
//! They are checked in, so a clone builds with them in hand. But they are made
//! FROM this crate — zipc reads the manifest the compiled binary prints — so
//! the first build of a new service necessarily happens before they exist. This
//! says so instead of failing: no documents yet, and `make gen` makes them.

use std::path::PathBuf;

fn main() {
    println!("cargo:rerun-if-changed=gen");
    let out = PathBuf::from(std::env::var("OUT_DIR").expect("OUT_DIR"));
    let mut body = String::new();
    for (name, file) in [("OPENAPI", "gen/openapi.json"), ("MCP", "gen/mcp.json")] {
        let text = std::fs::read_to_string(file).unwrap_or_default();
        body.push_str(&format!(
            "pub const {name}: &str = {:?};\n",
            if text.is_empty() { "" } else { text.as_str() }
        ));
    }
    std::fs::write(out.join("documents.rs"), body).expect("write documents.rs");
}
