// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! Where the description goes.
//!
//! A macro runs inside the compiler and writes its fragment of the manifest as
//! it expands, so the description of an op exists the moment the op does. There
//! is no second pass over the source, no generated file to check in, and no
//! staleness to gate: what the compiler read is what the projector reads.
//!
//! The fragments land under `ZIP_MANIFEST_DIR`, or `target/zip` beside the
//! crate. `zipc` merges them.

use std::fs;
use std::path::PathBuf;

/// dir is where this crate's fragments go, creating it if it is not there.
pub fn dir(kind: &str) -> Option<PathBuf> {
    let base = match std::env::var("ZIP_MANIFEST_DIR") {
        Ok(v) if !v.is_empty() => PathBuf::from(v),
        _ => PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").ok()?)
            .join("target")
            .join("zip"),
    };
    let at = base.join(kind);
    fs::create_dir_all(&at).ok()?;
    Some(at)
}

/// write files one fragment, named so a second expansion of the same item
/// replaces the first rather than adding to it.
pub fn write(kind: &str, name: &str, body: &str) {
    if let Some(at) = dir(kind) {
        let _ = fs::write(at.join(format!("{name}.json")), body);
    }
}

/// quote is a JSON string literal.
pub fn quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// field writes `"name": value` when value is worth writing.
pub struct Object {
    body: String,
}

impl Object {
    pub fn new() -> Self {
        Object {
            body: String::from("{"),
        }
    }
    pub fn raw(&mut self, name: &str, value: &str) -> &mut Self {
        if self.body.len() > 1 {
            self.body.push(',');
        }
        self.body.push_str(&quote(name));
        self.body.push(':');
        self.body.push_str(value);
        self
    }
    pub fn text(&mut self, name: &str, value: &str) -> &mut Self {
        if value.is_empty() {
            return self;
        }
        self.raw(name, &quote(value))
    }
    pub fn flag(&mut self, name: &str, value: bool) -> &mut Self {
        if !value {
            return self;
        }
        self.raw(name, "true")
    }
    pub fn number(&mut self, name: &str, value: usize) -> &mut Self {
        if value == 0 {
            return self;
        }
        self.raw(name, &value.to_string())
    }
    pub fn finish(&mut self) -> String {
        let mut out = std::mem::take(&mut self.body);
        out.push('}');
        out
    }
}

/// list is a JSON array of already-rendered values.
pub fn list(items: &[String]) -> String {
    format!("[{}]", items.join(","))
}
