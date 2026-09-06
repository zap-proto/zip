// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The frames this door speaks are the frames everyone else speaks.
//!
//! A door tested only by its own client passes whatever it does. That is how
//! this one shipped with the frame's length prefix written little-endian while
//! zap-proto/http writes it big-endian: both ends of the test agreed, and no Go
//! client could reach the service at all.
//!
//! So the bytes are pinned against frames another implementation wrote.
//! testdata/zaphttp/go.request and go.response were produced by
//! zap-proto/http's own encoder — the reference — and are read here without
//! any Go running. The same files are asserted from the Go side in
//! zaphttp_wire_test.go, which is what keeps them current: one file, checked
//! from both directions, and a change to either implementation that the other
//! does not follow fails on one side or the other.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;

/// golden is one frame another implementation wrote.
fn golden(name: &str) -> Vec<u8> {
    let at = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/zaphttp")
        .join(name);
    std::fs::read(&at).unwrap_or_else(|e| panic!("{}: {e}", at.display()))
}

#[test]
fn a_frame_the_reference_wrote_reads_here() {
    let frame = golden("go.request");
    let r = zip::wire::Request::wrap(&frame).expect("the reference's request frame reads");
    assert_eq!(r.method(), "GET");
    assert_eq!(r.target(), "/node/version");
    assert_eq!(r.body(), b"");
}

#[test]
fn an_answer_the_reference_wrote_reads_here() {
    let frame = golden("go.response");
    let r = zip::wire::Response::wrap(&frame).expect("the reference's response frame reads");
    assert_eq!(r.status(), 200);
    assert_eq!(r.body(), br#"{"ok":true}"#);
    // The header block: a count, then that many length-prefixed pairs. The one
    // header a reply carries is what it is.
    let raw = r.headers();
    assert_eq!(u32::from_le_bytes([raw[0], raw[1], raw[2], raw[3]]), 1);
}

/// pin holds what this side writes, or checks it, so the two are one act.
/// Set ZIP_GOLDEN to rewrite them after a deliberate change to the wire.
fn pin(name: &str, got: &[u8]) {
    let at = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/zaphttp")
        .join(name);
    if std::env::var_os("ZIP_GOLDEN").is_some() {
        std::fs::write(&at, got).unwrap();
        return;
    }
    assert_eq!(got, golden(name).as_slice(), "{name} moved");
}

#[test]
fn the_frames_this_door_writes_are_pinned() {
    // What this side writes is checked in beside what the reference writes, so
    // a change to either shows up as a diff rather than as a service nobody
    // can reach.
    pin(
        "rust.request",
        &zip::zaphttp::ask("GET", "/node/version", &[]),
    );
    pin(
        "rust.response",
        &zip::zaphttp::reply(200, "application/json", br#"{"ok":true}"#),
    );
}

#[test]
fn the_prefix_is_the_transports_and_is_big_endian() {
    assert_eq!(zip::zaphttp::prefix(88), [0x00, 0x00, 0x00, 0x58]);
    assert_eq!(zip::zaphttp::length([0x00, 0x00, 0x00, 0x58]), 88);
}

#[test]
fn the_door_answers_the_reference_frame() {
    let mut app = zip::App::new("probe", "", "", "");
    app.serve(
        "/node/version",
        "application/json",
        r#"{"version":"probe"}"#,
    );
    let app = Arc::new(app);

    let door = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let at = door.local_addr().unwrap().to_string();
    drop(door);
    {
        let app = Arc::clone(&app);
        let at = at.clone();
        std::thread::spawn(move || {
            let _ = zip::zaphttp::listen(app, &at);
        });
    }
    let mut c = None;
    for _ in 0..200 {
        if let Ok(s) = TcpStream::connect(&at) {
            c = Some(s);
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    let mut c = c.expect("the door came up");

    // The request is the reference's bytes, sent under the reference's prefix.
    let frame = golden("go.request");
    c.write_all(&zip::zaphttp::prefix(frame.len())).unwrap();
    c.write_all(&frame).unwrap();

    let mut head = [0u8; 4];
    c.read_exact(&mut head).unwrap();
    let mut body = vec![0u8; zip::zaphttp::length(head)];
    c.read_exact(&mut body).unwrap();
    let r = zip::wire::Response::wrap(&body).unwrap();
    assert_eq!(r.status(), 200);
    assert_eq!(r.body(), br#"{"version":"probe"}"#);
}
