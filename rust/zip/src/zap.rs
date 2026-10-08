// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The ZAP-HTTP frames: HTTP semantics on the ZAP wire, as zap-proto/http
//! (Go) writes them.
//!
//! A frame is a ZAP message: a 16-byte header (magic `ZAP\0`, version 1, flags
//! carrying the frame type in their upper byte, the root object's offset, the
//! message's size), then the root object's fixed slots, then the bytes the
//! slots point at. A text or bytes slot is eight bytes, a little-endian offset
//! relative to the slot and a length; an empty one is all zero.
//!
//! | frame | type | slots |
//! |---|---|---|
//! | request | 0x01 | method @0, target @8, proto @16, headers @24, body @32, trailer @40 |
//! | response | 0x02 | status u16 @0, reason @8, proto @16, headers @24, body @32, trailer @40 |
//! | streamed head | 0x03 | a response with no body and no trailer |
//! | data | 0x04 | one chunk of the body @0 |
//! | end | 0x05 | the trailer pairs @0 |
//!
//! Headers and trailers ride as a count, then that many length-prefixed name
//! and value runs, every integer little-endian. On a connection each frame is
//! preceded by its length as a BIG-endian u32, as zap-proto/http's transport
//! writes it.
//!
//! The offsets are zap-proto/http's (wire.go). No zapgen emits Rust, so they
//! are written here once and held to frames Go wrote, byte for byte, by the
//! tests at the bottom.

use bytes::Bytes;

pub const FRAME_REQUEST: u16 = 0x01;
pub const FRAME_RESPONSE: u16 = 0x02;
pub const FRAME_HEAD: u16 = 0x03;
pub const FRAME_DATA: u16 = 0x04;
pub const FRAME_END: u16 = 0x05;

const HEADER: usize = 16;
const MAGIC: &[u8; 4] = b"ZAP\0";
const VERSION: u16 = 1;

/// The protocol an answer names, which is what fasthttp reports for one.
const PROTO: &str = "HTTP/1.1";

/// Request is a request frame, read where it lies.
pub struct Request<'a> {
    frame: &'a [u8],
    root: usize,
    size: usize,
}

impl<'a> Request<'a> {
    pub fn wrap(frame: &'a [u8]) -> Option<Request<'a>> {
        let (root, size) = root(frame, FRAME_REQUEST)?;
        Some(Request { frame, root, size })
    }
    pub fn method(&self) -> &'a str {
        text(var(self.frame, self.size, self.root, 0))
    }
    pub fn target(&self) -> &'a str {
        text(var(self.frame, self.size, self.root, 8))
    }
    pub fn headers(&self) -> &'a [u8] {
        var(self.frame, self.size, self.root, 24)
    }
    pub fn body(&self) -> &'a [u8] {
        var(self.frame, self.size, self.root, 32)
    }
}

/// Response is a response frame or a streamed head, read where it lies.
pub struct Response<'a> {
    frame: &'a [u8],
    root: usize,
    size: usize,
}

impl<'a> Response<'a> {
    pub fn wrap(frame: &'a [u8]) -> Option<Response<'a>> {
        let (root, size) = root(frame, FRAME_RESPONSE).or_else(|| root(frame, FRAME_HEAD))?;
        Some(Response { frame, root, size })
    }
    pub fn status(&self) -> u16 {
        let at = self.root;
        if at + 2 > self.size {
            return 0;
        }
        u16::from_le_bytes([self.frame[at], self.frame[at + 1]])
    }
    pub fn headers(&self) -> &'a [u8] {
        var(self.frame, self.size, self.root, 24)
    }
    pub fn body(&self) -> &'a [u8] {
        var(self.frame, self.size, self.root, 32)
    }
    pub fn trailer(&self) -> &'a [u8] {
        var(self.frame, self.size, self.root, 40)
    }
}

/// kind is a frame's type, from its header alone.
pub fn kind(frame: &[u8]) -> Option<u16> {
    if frame.len() < HEADER || &frame[0..4] != MAGIC {
        return None;
    }
    Some(u16::from_le_bytes([frame[6], frame[7]]) >> 8)
}

/// chunk is a data or end frame's one slot.
pub fn chunk(frame: &[u8]) -> Option<&[u8]> {
    let (root, size) = root(frame, FRAME_DATA).or_else(|| root(frame, FRAME_END))?;
    Some(var(frame, size, root, 0))
}

/// root checks a frame's header — magic, version, size, type — and answers its
/// root object's offset and the message's length. Bytes past the size are not
/// the message's.
fn root(frame: &[u8], want: u16) -> Option<(usize, usize)> {
    if kind(frame)? != want {
        return None;
    }
    let version = u16::from_le_bytes([frame[4], frame[5]]);
    if version != 1 && version != 2 {
        return None;
    }
    let size = u32_at(frame, 12) as usize;
    if size < HEADER || size > frame.len() {
        return None;
    }
    let root = u32_at(frame, 8) as usize;
    if root < HEADER || root >= size {
        return None;
    }
    Some((root, size))
}

/// var is the bytes one slot points at, or none when the slot is empty or
/// points anywhere it may not: before the data, or past the message.
fn var(frame: &[u8], size: usize, root: usize, field: usize) -> &[u8] {
    let slot = root + field;
    if slot + 8 > size {
        return &[];
    }
    let rel = u32_at(frame, slot) as usize;
    if rel == 0 {
        return &[];
    }
    let len = u32_at(frame, slot + 4) as usize;
    let Some(at) = slot.checked_add(rel) else {
        return &[];
    };
    match at.checked_add(len) {
        Some(end) if at >= HEADER && end <= size => &frame[at..end],
        _ => &[],
    }
}

fn text(b: &[u8]) -> &str {
    std::str::from_utf8(b).unwrap_or("")
}

fn u32_at(b: &[u8], at: usize) -> u32 {
    u32::from_le_bytes([b[at], b[at + 1], b[at + 2], b[at + 3]])
}

/// pairs walks a header block. Every name and value is a slice of the frame; a
/// block that runs short ends where it ran short.
pub fn pairs(raw: &[u8]) -> Vec<(&[u8], &[u8])> {
    let mut out = Vec::new();
    if raw.len() < 4 {
        return out;
    }
    let count = u32_at(raw, 0) as usize;
    let mut at = 4;
    for _ in 0..count {
        let Some(name) = run(raw, &mut at) else { break };
        let Some(value) = run(raw, &mut at) else {
            break;
        };
        out.push((name, value));
    }
    out
}

fn run<'a>(raw: &'a [u8], at: &mut usize) -> Option<&'a [u8]> {
    if *at + 4 > raw.len() {
        return None;
    }
    let n = u32_at(raw, *at) as usize;
    *at += 4;
    let end = at.checked_add(n)?;
    if end > raw.len() {
        return None;
    }
    let s = &raw[*at..end];
    *at = end;
    Some(s)
}

/// block writes header pairs as a header block; no pairs is no block.
pub fn block<'a>(pairs: impl IntoIterator<Item = (&'a [u8], &'a [u8])>) -> Vec<u8> {
    let mut out = vec![0, 0, 0, 0];
    let mut n = 0u32;
    for (name, value) in pairs {
        out.extend_from_slice(&(name.len() as u32).to_le_bytes());
        out.extend_from_slice(name);
        out.extend_from_slice(&(value.len() as u32).to_le_bytes());
        out.extend_from_slice(value);
        n += 1;
    }
    if n == 0 {
        return Vec::new();
    }
    out[0..4].copy_from_slice(&n.to_le_bytes());
    out
}

/// Builder lays one frame out: the header, the fixed slots, then each field's
/// bytes in the order they are put.
struct Builder {
    buf: Vec<u8>,
}

impl Builder {
    fn new(kind: u16, slots: usize, room: usize) -> Builder {
        let mut buf = Vec::with_capacity(HEADER + slots + room);
        buf.extend_from_slice(MAGIC);
        buf.extend_from_slice(&VERSION.to_le_bytes());
        buf.extend_from_slice(&(kind << 8).to_le_bytes());
        buf.extend_from_slice(&(HEADER as u32).to_le_bytes());
        buf.extend_from_slice(&0u32.to_le_bytes());
        buf.resize(HEADER + slots, 0);
        Builder { buf }
    }

    fn u16(&mut self, field: usize, v: u16) {
        let at = HEADER + field;
        self.buf[at..at + 2].copy_from_slice(&v.to_le_bytes());
    }

    fn put(&mut self, field: usize, data: &[u8]) {
        if data.is_empty() {
            return;
        }
        let slot = HEADER + field;
        let rel = (self.buf.len() - slot) as u32;
        self.buf.extend_from_slice(data);
        self.buf[slot..slot + 4].copy_from_slice(&rel.to_le_bytes());
        self.buf[slot + 4..slot + 8].copy_from_slice(&(data.len() as u32).to_le_bytes());
    }

    fn finish(mut self) -> Vec<u8> {
        let size = self.buf.len() as u32;
        self.buf[12..16].copy_from_slice(&size.to_le_bytes());
        self.buf
    }
}

/// request builds a request frame: what a client sends.
pub fn request(method: &str, target: &str, headers: &[u8], body: &[u8]) -> Vec<u8> {
    let mut b = Builder::new(
        FRAME_REQUEST,
        48,
        method.len() + target.len() + headers.len() + body.len() + 8,
    );
    b.put(0, method.as_bytes());
    b.put(8, target.as_bytes());
    b.put(16, PROTO.as_bytes());
    b.put(24, headers);
    b.put(32, body);
    b.finish()
}

/// response builds an answer frame, whole.
pub fn response(status: u16, headers: &[u8], body: &[u8], trailer: &[u8]) -> Vec<u8> {
    let mut b = Builder::new(
        FRAME_RESPONSE,
        48,
        headers.len() + body.len() + trailer.len() + 32,
    );
    b.u16(0, status);
    b.put(8, crate::error::reason(status).as_bytes());
    b.put(16, PROTO.as_bytes());
    b.put(24, headers);
    b.put(32, body);
    b.put(40, trailer);
    b.finish()
}

/// head builds the opening frame of a streamed answer: its status and headers.
pub fn head(status: u16, headers: &[u8]) -> Vec<u8> {
    let mut b = Builder::new(FRAME_HEAD, 48, headers.len() + 32);
    b.u16(0, status);
    b.put(8, crate::error::reason(status).as_bytes());
    b.put(16, PROTO.as_bytes());
    b.put(24, headers);
    b.finish()
}

/// data builds one chunk of a streamed body.
pub fn data(chunk: &Bytes) -> Vec<u8> {
    let mut b = Builder::new(FRAME_DATA, 8, chunk.len());
    b.put(0, chunk);
    b.finish()
}

/// end builds the frame that ends a streamed body, carrying its trailers.
pub fn end(trailer: &[u8]) -> Vec<u8> {
    let mut b = Builder::new(FRAME_END, 8, trailer.len());
    b.put(0, trailer);
    b.finish()
}

#[cfg(test)]
mod tests {
    use super::*;

    // What zap-proto/http v0.3.12 writes (MarshalRequest, MarshalResponse,
    // MarshalResponseHead, MarshalData, MarshalEnd) for the same messages.
    const GO_REQUEST: &str = "5a41500001000001100000007d00000030000000040000002c0000000c00000030000000080000003000000018000000400000000d0000000000000000000000504f53542f76312f6563686f3f783d31485454502f312e310100000008000000582d4f72672d49640400000061636d657b2274657874223a226869227d";
    const GO_RESPONSE: &str = "5a41500001000002100000007f000000c800000000000000280000000200000022000000080000002200000028000000420000000d00000000000000000000004f4b485454502f312e31010000000c000000436f6e74656e742d54797065100000006170706c69636174696f6e2f6a736f6e7b2274657874223a226869227d";
    const GO_HEAD: &str = "5a415000010000031000000073000000c800000000000000280000000200000022000000080000002200000029000000000000000000000000000000000000004f4b485454502f312e31010000000c000000436f6e74656e742d5479706511000000746578742f6576656e742d73747265616d";
    const GO_DATA: &str = "5a4150000100000410000000210000000800000009000000646174613a20310a0a";
    const GO_END: &str = "5a4150000100000510000000180000000000000000000000";

    fn hex(b: &[u8]) -> String {
        b.iter().map(|x| format!("{x:02x}")).collect()
    }

    fn unhex(s: &str) -> Vec<u8> {
        (0..s.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
            .collect()
    }

    #[test]
    fn frames_are_the_ones_go_writes() {
        let h = block([(&b"X-Org-Id"[..], &b"acme"[..])]);
        assert_eq!(
            hex(&request("POST", "/v1/echo?x=1", &h, br#"{"text":"hi"}"#)),
            GO_REQUEST
        );
        let h = block([(&b"Content-Type"[..], &b"application/json"[..])]);
        assert_eq!(
            hex(&response(200, &h, br#"{"text":"hi"}"#, &[])),
            GO_RESPONSE
        );
        let h = block([(&b"Content-Type"[..], &b"text/event-stream"[..])]);
        assert_eq!(hex(&head(200, &h)), GO_HEAD);
        assert_eq!(hex(&data(&Bytes::from_static(b"data: 1\n\n"))), GO_DATA);
        assert_eq!(hex(&end(&[])), GO_END);
    }

    #[test]
    fn a_go_request_reads() {
        let f = unhex(GO_REQUEST);
        let r = Request::wrap(&f).unwrap();
        assert_eq!(r.method(), "POST");
        assert_eq!(r.target(), "/v1/echo?x=1");
        assert_eq!(pairs(r.headers()), vec![(&b"X-Org-Id"[..], &b"acme"[..])]);
        assert_eq!(r.body(), br#"{"text":"hi"}"#);
        assert!(Response::wrap(&f).is_none(), "a request is not an answer");
        let f = unhex(GO_HEAD);
        assert_eq!(Response::wrap(&f).unwrap().status(), 200);
        assert_eq!(chunk(&unhex(GO_DATA)).unwrap(), b"data: 1\n\n");
        assert_eq!(kind(&unhex(GO_END)), Some(FRAME_END));
    }

    #[test]
    fn a_crooked_frame_reads_as_nothing() {
        let mut f = unhex(GO_REQUEST);
        // A slot pointing past the message.
        f[HEADER + 8] = 0xff;
        let r = Request::wrap(&f).unwrap();
        assert_eq!(r.target(), "");
        assert!(Request::wrap(&f[..10]).is_none());
        let mut g = unhex(GO_REQUEST);
        g[0] = b'X';
        assert!(Request::wrap(&g).is_none());
        // A header block whose count overstates what it holds.
        assert!(pairs(&[9, 0, 0, 0, 1, 0, 0, 0]).is_empty());
    }
}
