// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! Writing the description down.
//!
//! A macro runs inside the compiler, and what it learns has to reach the
//! projector somehow. It reaches it as a `&'static str` COMPILED INTO the
//! crate, which the service prints on request — not as a file written during
//! expansion, which was the first shape and the wrong one: rustc reuses a
//! cached expansion, so a macro that did not run left its file saying what the
//! source used to say, and a document that quietly disagrees with the code is
//! the one failure this whole design exists to prevent.
//!
//! So: JSON, built from tokens, small enough to have no machinery.

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
