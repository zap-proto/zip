// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! JSON, as far as an API needs it.
//!
//! A value that arrived and a value being written, and nothing between them: no
//! derive of its own to keep in step with [`crate::Wire`], and no second
//! annotation on a type that has already said what its fields are called. The
//! wire name a document publishes is the wire name this reads, because there is
//! one place either is written down.

use crate::Error;
use std::collections::HashMap;

/// Json is a value that arrived.
#[derive(Clone, Debug, PartialEq)]
pub enum Json {
    Null,
    Bool(bool),
    Number(f64),
    Text(String),
    List(Vec<Json>),
    Map(HashMap<String, Json>),
}

impl Json {
    /// get is the named member of an object, or None.
    pub fn get(&self, name: &str) -> Option<&Json> {
        match self {
            Json::Map(m) => m.get(name),
            _ => None,
        }
    }

    pub fn is_null(&self) -> bool {
        matches!(self, Json::Null)
    }

    /// text is this value as a string, refusing anything else.
    pub fn text(&self) -> Result<&str, Error> {
        match self {
            Json::Text(s) => Ok(s),
            _ => Err(Error::bad("expected a string")),
        }
    }

    /// parse reads one value. Trailing bytes are a refusal: a body with a
    /// second document in it is a body someone else wrote.
    pub fn parse(src: &str) -> Result<Json, Error> {
        let b = src.as_bytes();
        let mut at = 0;
        let v = value(b, &mut at)?;
        skip(b, &mut at);
        if at != b.len() {
            return Err(Error::bad("trailing bytes after the value"));
        }
        Ok(v)
    }
}

fn skip(b: &[u8], at: &mut usize) {
    while *at < b.len() && matches!(b[*at], b' ' | b'\t' | b'\n' | b'\r') {
        *at += 1;
    }
}

fn value(b: &[u8], at: &mut usize) -> Result<Json, Error> {
    skip(b, at);
    match b.get(*at) {
        None => Err(Error::bad("the value ended early")),
        Some(b'{') => object(b, at),
        Some(b'[') => array(b, at),
        Some(b'"') => Ok(Json::Text(string(b, at)?)),
        Some(b't') => word(b, at, "true", Json::Bool(true)),
        Some(b'f') => word(b, at, "false", Json::Bool(false)),
        Some(b'n') => word(b, at, "null", Json::Null),
        _ => number(b, at),
    }
}

fn word(b: &[u8], at: &mut usize, want: &str, v: Json) -> Result<Json, Error> {
    if b.len() >= *at + want.len() && &b[*at..*at + want.len()] == want.as_bytes() {
        *at += want.len();
        return Ok(v);
    }
    Err(Error::bad("not a value"))
}

fn object(b: &[u8], at: &mut usize) -> Result<Json, Error> {
    *at += 1;
    let mut m = HashMap::new();
    skip(b, at);
    if b.get(*at) == Some(&b'}') {
        *at += 1;
        return Ok(Json::Map(m));
    }
    loop {
        skip(b, at);
        let k = string(b, at)?;
        skip(b, at);
        if b.get(*at) != Some(&b':') {
            return Err(Error::bad("a member needs a colon"));
        }
        *at += 1;
        m.insert(k, value(b, at)?);
        skip(b, at);
        match b.get(*at) {
            Some(b',') => *at += 1,
            Some(b'}') => {
                *at += 1;
                return Ok(Json::Map(m));
            }
            _ => return Err(Error::bad("the object ended early")),
        }
    }
}

fn array(b: &[u8], at: &mut usize) -> Result<Json, Error> {
    *at += 1;
    let mut out = Vec::new();
    skip(b, at);
    if b.get(*at) == Some(&b']') {
        *at += 1;
        return Ok(Json::List(out));
    }
    loop {
        out.push(value(b, at)?);
        skip(b, at);
        match b.get(*at) {
            Some(b',') => *at += 1,
            Some(b']') => {
                *at += 1;
                return Ok(Json::List(out));
            }
            _ => return Err(Error::bad("the list ended early")),
        }
    }
}

fn string(b: &[u8], at: &mut usize) -> Result<String, Error> {
    if b.get(*at) != Some(&b'"') {
        return Err(Error::bad("expected a string"));
    }
    *at += 1;
    let mut out = String::new();
    while let Some(&c) = b.get(*at) {
        *at += 1;
        match c {
            b'"' => return Ok(out),
            b'\\' => {
                let e = *b
                    .get(*at)
                    .ok_or_else(|| Error::bad("the string ended early"))?;
                *at += 1;
                match e {
                    b'"' => out.push('"'),
                    b'\\' => out.push('\\'),
                    b'/' => out.push('/'),
                    b'b' => out.push('\u{8}'),
                    b'f' => out.push('\u{c}'),
                    b'n' => out.push('\n'),
                    b'r' => out.push('\r'),
                    b't' => out.push('\t'),
                    b'u' => {
                        let n = hex(b, at)?;
                        // A surrogate pair is two escapes for one character, so
                        // the low half is read here rather than left to become a
                        // replacement character downstream.
                        let c = if (0xD800..0xDC00).contains(&n) {
                            if b.get(*at) == Some(&b'\\') && b.get(*at + 1) == Some(&b'u') {
                                *at += 2;
                                let lo = hex(b, at)?;
                                0x10000 + ((n - 0xD800) << 10) + (lo - 0xDC00)
                            } else {
                                n
                            }
                        } else {
                            n
                        };
                        out.push(char::from_u32(c).unwrap_or('\u{fffd}'));
                    }
                    _ => return Err(Error::bad("unknown escape")),
                }
            }
            c if c < 0x80 => out.push(c as char),
            _ => {
                // A multi-byte character: take its bytes whole rather than
                // one at a time, since a byte of one is not a character.
                let start = *at - 1;
                let mut end = *at;
                while end < b.len() && b[end] & 0xC0 == 0x80 {
                    end += 1;
                }
                out.push_str(
                    std::str::from_utf8(&b[start..end]).map_err(|_| Error::bad("not utf-8"))?,
                );
                *at = end;
            }
        }
    }
    Err(Error::bad("the string ended early"))
}

fn hex(b: &[u8], at: &mut usize) -> Result<u32, Error> {
    if *at + 4 > b.len() {
        return Err(Error::bad("a \\u escape is four digits"));
    }
    let s = std::str::from_utf8(&b[*at..*at + 4]).map_err(|_| Error::bad("not utf-8"))?;
    *at += 4;
    u32::from_str_radix(s, 16).map_err(|_| Error::bad("a \\u escape is four hex digits"))
}

fn number(b: &[u8], at: &mut usize) -> Result<Json, Error> {
    let start = *at;
    while let Some(&c) = b.get(*at) {
        if c.is_ascii_digit() || matches!(c, b'-' | b'+' | b'.' | b'e' | b'E') {
            *at += 1;
        } else {
            break;
        }
    }
    let s = std::str::from_utf8(&b[start..*at]).map_err(|_| Error::bad("not utf-8"))?;
    s.parse::<f64>()
        .map(Json::Number)
        .map_err(|_| Error::bad("not a number"))
}

// ---- writing ---------------------------------------------------------------

/// write_str appends one JSON string.
pub fn write_str(out: &mut String, s: &str) {
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
}

/// write_float appends a number. A value JSON cannot hold is written as null,
/// which is the only true thing to say about it.
pub fn write_float(out: &mut String, v: f64) {
    if !v.is_finite() {
        out.push_str("null");
        return;
    }
    if v == v.trunc() && v.abs() < 1e15 {
        out.push_str(&format!("{}", v as i64));
        return;
    }
    out.push_str(&format!("{v}"));
}

/// write_bytes appends a byte string, base64 as JSON carries one.
pub fn write_bytes(out: &mut String, b: &[u8]) {
    const A: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut s = String::with_capacity(b.len().div_ceil(3) * 4);
    for c in b.chunks(3) {
        let n = (c[0] as u32) << 16
            | (*c.get(1).unwrap_or(&0) as u32) << 8
            | (*c.get(2).unwrap_or(&0) as u32);
        s.push(A[(n >> 18 & 63) as usize] as char);
        s.push(A[(n >> 12 & 63) as usize] as char);
        s.push(if c.len() > 1 {
            A[(n >> 6 & 63) as usize] as char
        } else {
            '='
        });
        s.push(if c.len() > 2 {
            A[(n & 63) as usize] as char
        } else {
            '='
        });
    }
    write_str(out, &s);
}

/// read_bytes is base64, back.
pub fn read_bytes(s: &str) -> Result<Vec<u8>, Error> {
    let mut out = Vec::new();
    let mut buf = 0u32;
    let mut have = 0;
    for c in s.bytes() {
        let v = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' => 62,
            b'/' => 63,
            b'=' | b'\n' | b'\r' => continue,
            _ => return Err(Error::bad("not base64")),
        } as u32;
        buf = buf << 6 | v;
        have += 6;
        if have >= 8 {
            have -= 8;
            out.push((buf >> have) as u8);
        }
    }
    Ok(out)
}

// ---- what a derived reader asks -------------------------------------------
//
// A field the body did not carry reads as its zero rather than as a refusal:
// that is what every other decoder on this wire does, and a required field is
// refused by validation, which is one rule in one place.

pub fn as_text(v: Option<&Json>) -> Result<String, Error> {
    match v {
        Some(Json::Text(s)) => Ok(s.clone()),
        Some(Json::Null) | None => Ok(String::new()),
        Some(_) => Err(Error::bad("expected a string")),
    }
}

pub fn as_bool(v: Option<&Json>) -> Result<bool, Error> {
    match v {
        Some(Json::Bool(b)) => Ok(*b),
        Some(Json::Null) | None => Ok(false),
        Some(_) => Err(Error::bad("expected a boolean")),
    }
}

pub fn as_number(v: Option<&Json>) -> Result<f64, Error> {
    match v {
        Some(Json::Number(n)) => Ok(*n),
        Some(Json::Null) | None => Ok(0.0),
        Some(_) => Err(Error::bad("expected a number")),
    }
}

pub fn as_bytes(v: Option<&Json>) -> Result<Vec<u8>, Error> {
    match v {
        Some(Json::Text(s)) => read_bytes(s),
        Some(Json::Null) | None => Ok(Vec::new()),
        Some(_) => Err(Error::bad("expected base64")),
    }
}

pub fn as_list(v: Option<&Json>) -> Result<&[Json], Error> {
    match v {
        Some(Json::List(l)) => Ok(l),
        Some(Json::Null) | None => Ok(&[]),
        Some(_) => Err(Error::bad("expected a list")),
    }
}

/// EMPTY is what a map field reads as when the body did not carry one.
static EMPTY: std::sync::OnceLock<HashMap<String, Json>> = std::sync::OnceLock::new();

pub fn as_map(v: Option<&Json>) -> Result<&HashMap<String, Json>, Error> {
    match v {
        Some(Json::Map(m)) => Ok(m),
        Some(Json::Null) | None => Ok(EMPTY.get_or_init(HashMap::new)),
        Some(_) => Err(Error::bad("expected an object")),
    }
}

/// as_wire reads a value that describes itself.
pub fn as_wire<T: crate::Wire>(v: Option<&Json>) -> Result<T, Error> {
    T::read_json(v.unwrap_or(&Json::Null))
}
