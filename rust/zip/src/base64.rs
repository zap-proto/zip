// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! A byte field, as JSON carries one: standard base64 in a string.
//!
//! serde writes a `Vec<u8>` as an array of numbers, which is not what the
//! document says a byte field is (`string`, `contentEncoding: base64`) nor what
//! Go's encoder writes. A byte field therefore names this module, and the
//! derive refuses one that does not:
//!
//! ```ignore
//! #[serde(with = "zip::base64")]
//! pub payload: Vec<u8>,
//! ```

use serde::{Deserialize, Deserializer, Serializer};

const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

pub fn serialize<S: Serializer>(bytes: &[u8], s: S) -> Result<S::Ok, S::Error> {
    s.serialize_str(&encode(bytes))
}

pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<Vec<u8>, D::Error> {
    let text = String::deserialize(d)?;
    decode(&text).map_err(serde::de::Error::custom)
}

/// encode is bytes as base64, padded.
pub fn encode(b: &[u8]) -> String {
    let mut s = String::with_capacity(b.len().div_ceil(3) * 4);
    for c in b.chunks(3) {
        let n = (c[0] as u32) << 16
            | (*c.get(1).unwrap_or(&0) as u32) << 8
            | (*c.get(2).unwrap_or(&0) as u32);
        s.push(ALPHABET[(n >> 18 & 63) as usize] as char);
        s.push(ALPHABET[(n >> 12 & 63) as usize] as char);
        s.push(if c.len() > 1 {
            ALPHABET[(n >> 6 & 63) as usize] as char
        } else {
            '='
        });
        s.push(if c.len() > 2 {
            ALPHABET[(n & 63) as usize] as char
        } else {
            '='
        });
    }
    s
}

/// decode is base64, back. Padding and line breaks are skipped; any other
/// byte outside the alphabet is a refusal.
pub fn decode(s: &str) -> Result<Vec<u8>, &'static str> {
    let mut out = Vec::with_capacity(s.len() / 4 * 3);
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
            _ => return Err("not base64"),
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips() {
        for n in 0..10 {
            let b: Vec<u8> = (0..n).map(|i| (i * 37 + 11) as u8).collect();
            assert_eq!(decode(&encode(&b)).unwrap(), b);
        }
        assert_eq!(encode(b"zip"), "emlw");
        assert_eq!(encode(b"zi"), "emk=");
        assert!(decode("a*b").is_err());
    }
}
