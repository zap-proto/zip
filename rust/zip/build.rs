// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The wire is generated, here, before the crate compiles.
//!
//! `zapgen` reads wire.zap and writes the ZAP runtime and the accessors for the
//! two frames this door speaks. It is a build-time tool and nothing it produces
//! carries a dependency: what ships is Rust, and what runs is Rust.

use std::path::PathBuf;
use std::process::Command;

fn main() {
    println!("cargo:rerun-if-changed=wire.zap");
    println!("cargo:rerun-if-env-changed=ZAPGEN");

    let out = PathBuf::from(std::env::var("OUT_DIR").expect("OUT_DIR"));
    let zapgen = std::env::var("ZAPGEN").unwrap_or_else(|_| "zapgen".to_string());
    let run = Command::new(&zapgen)
        .args(["-lang", "rust", "-out"])
        .arg(&out)
        .arg("wire.zap")
        .output();
    match run {
        Ok(r) if r.status.success() => {
            // One file holding both modules, because `include!` pastes tokens
            // and a pasted file cannot carry the inner attributes a generated
            // module begins with. Braced, they are ordinary items again.
            let mut all =
                String::from("// Written by zip's build script from what zapgen emitted.\n");
            for (name, file) in [("zap", "zap.rs"), ("wire", "wire_zap.rs")] {
                let body = std::fs::read_to_string(out.join(file))
                    .unwrap_or_else(|e| panic!("zapgen wrote no {file}: {e}"));
                // The style of generated code is the generator's business: a
                // lint answered here would be answered again in every crate
                // zapgen writes for, and the fix belongs in zapgen or nowhere.
                all.push_str(&format!(
                    "pub mod {name} {{\n#![allow(clippy::derivable_impls)]\n{body}\n}}\n"
                ));
            }
            std::fs::write(out.join("generated.rs"), all).expect("write generated.rs");
        }
        Ok(r) => panic!(
            "zapgen refused wire.zap:\n{}",
            String::from_utf8_lossy(&r.stderr)
        ),
        Err(e) => panic!(
            "zip needs zapgen to generate its wire ({zapgen}: {e}).\n\
             Build it from zap-proto/go (cmd/zapgen) and put it on PATH, or set ZAPGEN to it."
        ),
    }
}
