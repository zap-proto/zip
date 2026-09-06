// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The ZAP door.
//!
//! The same requests, carried as ZAP frames instead of as text. A frame is one
//! message: a four-byte length, then a ZAP buffer whose header flags say
//! whether it is a request or an answer, and whose root object holds the method,
//! the target, the headers and the body at fixed offsets.
//!
//! Nothing here decodes. A field is a bounds-checked look at bytes already in
//! hand — `target()` is a `&str` INTO the frame — so a request arrives without
//! being copied into a shape first. What is built is built once, into one
//! buffer, by the builder the generator ships.
//!
//! The offsets are the ones the ZAP-HTTP schema states (wire.zap), and the
//! accessors that read them are generated from it. There is no hand-written
//! wire in this file.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;

use crate::wire::{self, Request};
use crate::{zap, App};

/// The frame type, carried in the ZAP header's flags. A request and an answer
/// are the two shapes this door speaks.
pub const FRAME_REQUEST: u16 = 0x01;
pub const FRAME_RESPONSE: u16 = 0x02;

/// The largest frame this door will read. A length prefix is a promise from
/// whoever sent it, and a door that believes one has handed the peer a way to
/// ask for all the memory there is.
pub const MAX_FRAME: usize = 32 << 20;

/// listen serves app over ZAP at addr, and does not return.
pub fn listen(app: Arc<App>, addr: &str) -> std::io::Result<()> {
    let door = TcpListener::bind(addr)?;
    for conn in door.incoming() {
        let Ok(conn) = conn else { continue };
        let app = Arc::clone(&app);
        std::thread::spawn(move || {
            let _ = serve(&app, conn);
        });
    }
    Ok(())
}

fn serve(app: &App, mut conn: TcpStream) -> std::io::Result<()> {
    let mut out = conn.try_clone()?;
    loop {
        let Some(frame) = read_frame(&mut conn)? else {
            return Ok(());
        };
        let answer = match Request::wrap(&frame) {
            Ok(r) => {
                let headers = pairs(r.headers());
                app.answer(r.method(), r.target(), headers, r.body().to_vec())
            }
            Err(_) => crate::Answer {
                status: 400,
                kind: "application/problem+json",
                body: crate::http::refuse(&crate::Error::bad("malformed request frame")),
            },
        };
        let body = build(answer.status, answer.kind, &answer.body);
        out.write_all(&(body.len() as u32).to_le_bytes())?;
        out.write_all(&body)?;
        out.flush()?;
    }
}

/// read_frame takes one length-prefixed frame off the connection.
fn read_frame(conn: &mut TcpStream) -> std::io::Result<Option<Vec<u8>>> {
    let mut head = [0u8; 4];
    match conn.read_exact(&mut head) {
        Ok(()) => {}
        Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => return Ok(None),
        Err(e) => return Err(e),
    }
    let n = u32::from_le_bytes(head) as usize;
    if n == 0 || n > MAX_FRAME {
        return Ok(None);
    }
    let mut frame = vec![0u8; n];
    conn.read_exact(&mut frame)?;
    Ok(Some(frame))
}

/// pairs walks the header block: a count, then that many length-prefixed name
/// and value runs. Every name and value is a slice of the frame.
fn pairs(raw: &[u8]) -> Vec<(String, String)> {
    let mut out = Vec::new();
    if raw.len() < 4 {
        return out;
    }
    let count = u32::from_le_bytes([raw[0], raw[1], raw[2], raw[3]]) as usize;
    let mut at = 4;
    for _ in 0..count {
        let Some(name) = run(raw, &mut at) else { break };
        let Some(value) = run(raw, &mut at) else {
            break;
        };
        out.push((
            String::from_utf8_lossy(name).into_owned(),
            String::from_utf8_lossy(value).into_owned(),
        ));
    }
    out
}

fn run<'a>(raw: &'a [u8], at: &mut usize) -> Option<&'a [u8]> {
    if *at + 4 > raw.len() {
        return None;
    }
    let n = u32::from_le_bytes([raw[*at], raw[*at + 1], raw[*at + 2], raw[*at + 3]]) as usize;
    *at += 4;
    if *at + n > raw.len() {
        return None;
    }
    let s = &raw[*at..*at + n];
    *at += n;
    Some(s)
}

/// build writes one answer frame: the status, the content type as the one
/// header, and the body.
fn build(status: u16, kind: &str, body: &[u8]) -> Vec<u8> {
    let mut headers = Vec::new();
    headers.extend_from_slice(&1u32.to_le_bytes());
    push(&mut headers, b"Content-Type");
    push(&mut headers, kind.as_bytes());

    let reason = crate::http::reason(status);
    let mut b = zap::Builder::new(body.len() + headers.len() + 256);
    let mut ob = b.start_object(wire::RESPONSE_SIZE);
    ob.set_u16(&mut b, wire::RESPONSE_STATUS, status);
    ob.set_text(&mut b, wire::RESPONSE_REASON, reason);
    ob.set_text(&mut b, wire::RESPONSE_PROTO, "ZAP-HTTP/1.0");
    ob.set_bytes(&mut b, wire::RESPONSE_HEADERS, &headers);
    ob.set_bytes(&mut b, wire::RESPONSE_BODY, body);
    ob.set_bytes(&mut b, wire::RESPONSE_TRAILER, &[]);
    ob.finish_as_root(&mut b);
    b.finish_with_flags(FRAME_RESPONSE << 8)
}

fn push(out: &mut Vec<u8>, s: &[u8]) {
    out.extend_from_slice(&(s.len() as u32).to_le_bytes());
    out.extend_from_slice(s);
}

/// ask builds one request frame — what a client sends, and what the tests read
/// back to prove the door answers ZAP and not only text.
pub fn ask(method: &str, target: &str, body: &[u8]) -> Vec<u8> {
    let mut b = zap::Builder::new(body.len() + 256);
    let mut ob = b.start_object(wire::REQUEST_SIZE);
    ob.set_text(&mut b, wire::REQUEST_METHOD, method);
    ob.set_text(&mut b, wire::REQUEST_TARGET, target);
    ob.set_text(&mut b, wire::REQUEST_PROTO, "ZAP-HTTP/1.0");
    ob.set_bytes(&mut b, wire::REQUEST_HEADERS, &[]);
    ob.set_bytes(&mut b, wire::REQUEST_BODY, body);
    ob.set_bytes(&mut b, wire::REQUEST_TRAILER, &[]);
    ob.finish_as_root(&mut b);
    b.finish_with_flags(FRAME_REQUEST << 8)
}
