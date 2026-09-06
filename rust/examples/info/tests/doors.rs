// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The two doors answer the same ops.
//!
//! A REST request and a ZAP frame reach one handler, so the test asks both and
//! compares what came back. It is the only way to know the claim is true: two
//! doors that each pass their own test can still be two services.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;

fn doors() -> (String, String) {
    let app = Arc::new(info::service("luxd/1.36.178", 96369));
    let http = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let zap = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let (ha, za) = (
        http.local_addr().unwrap().to_string(),
        zap.local_addr().unwrap().to_string(),
    );
    drop(http);
    drop(zap);
    for (addr, over) in [(ha.clone(), true), (za.clone(), false)] {
        let app = Arc::clone(&app);
        std::thread::spawn(move || {
            if over {
                let _ = zip::http::listen(app, &addr);
            } else {
                let _ = zip::zaphttp::listen(app, &addr);
            }
        });
    }
    for _ in 0..200 {
        if TcpStream::connect(&ha).is_ok() && TcpStream::connect(&za).is_ok() {
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    (ha, za)
}

/// over_http asks one question in text and answers with the body.
fn over_http(addr: &str, target: &str) -> (u16, String) {
    let mut c = TcpStream::connect(addr).unwrap();
    let req = format!("GET {target} HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    c.write_all(req.as_bytes()).unwrap();
    let mut all = String::new();
    c.read_to_string(&mut all).unwrap();
    let status: u16 = all.split_whitespace().nth(1).unwrap().parse().unwrap();
    let body = all.split("\r\n\r\n").nth(1).unwrap_or("").to_string();
    (status, body)
}

/// over_zap asks the same question in ZAP frames and answers with the body.
fn over_zap(addr: &str, target: &str) -> (u16, String) {
    let mut c = TcpStream::connect(addr).unwrap();
    let frame = zip::zaphttp::ask("GET", target, &[]);
    c.write_all(&zip::zaphttp::prefix(frame.len())).unwrap();
    c.write_all(&frame).unwrap();
    let mut head = [0u8; 4];
    c.read_exact(&mut head).unwrap();
    let mut body = vec![0u8; zip::zaphttp::length(head)];
    c.read_exact(&mut body).unwrap();

    // The answer is a ZAP message: the magic is there, the flags say what kind
    // of frame it is, and the fields are read where they lie.
    assert_eq!(&body[0..4], b"ZAP\0", "the answer is not a ZAP message");
    let flags = u16::from_le_bytes([body[6], body[7]]);
    assert_eq!(
        flags >> 8,
        zip::zaphttp::FRAME_RESPONSE,
        "not a response frame"
    );

    let r = zip::wire::Response::wrap(&body).unwrap();
    (r.status(), String::from_utf8(r.body().to_vec()).unwrap())
}

#[test]
fn both_doors_answer_the_same_op() {
    let (http, zap) = doors();
    for target in [
        "/node/version",
        "/chains",
        "/chain/bootstrapped?chain=X",
        "/peers",
        "/lps",
        "/vms",
        "/fees",
    ] {
        let a = over_http(&http, target);
        let b = over_zap(&zap, target);
        assert_eq!(a.0, 200, "http {target}: {}", a.1);
        assert_eq!(a, b, "the two doors disagree about {target}");
    }
}

/// A quoted decimal is a string on the wire and eight bytes to a layout, and
/// this is the half a reader sees.
#[test]
fn a_count_is_carried_as_text() {
    let (http, _) = doors();
    let (status, body) = over_http(&http, "/fees");
    assert_eq!(status, 200);
    assert!(body.contains(r#""txFee":"1000000""#), "{body}");
}

/// The URL is the whole input of a bodyless op, so a query value binds.
#[test]
fn a_query_value_binds() {
    let (http, _) = doors();
    assert_eq!(
        over_http(&http, "/chain/bootstrapped?chain=X"),
        (200, r#"{"isBootstrapped":true}"#.to_string())
    );
    // A field the op declared it cannot run without is refused by the binder,
    // in the words the document already used for it, before the handler runs.
    let (status, body) = over_http(&http, "/chain/bootstrapped");
    assert_eq!(status, 400, "{body}");
    assert_eq!(
        body,
        "{\"detail\":\"field \\\"chain\\\" is required\",\"status\":400,\"title\":\"Bad Request\",\"type\":\"about:blank\"}"
    );
}

/// A list rides one URL value, comma-separated, which is what the document
/// already publishes for a repeated parameter.
#[test]
fn a_list_rides_one_value() {
    let (http, _) = doors();
    let (status, body) = over_http(&http, "/peers?nodeIDs=NodeID-a,NodeID-b");
    assert_eq!(status, 200, "{body}");
    assert!(body.starts_with(r#"{"numPeers":"1""#), "{body}");
}

/// The service publishes the document it implements.
#[test]
fn the_document_is_served() {
    let (http, _) = doors();
    let (status, body) = over_http(&http, "/openapi.json");
    assert_eq!(status, 200);
    assert!(
        body.contains(r#""operationId": "get_chain_bootstrapped""#),
        "{body}"
    );
    let (status, tools) = over_http(&http, "/mcp.json");
    assert_eq!(status, 200);
    assert!(tools.contains(r#""name": "get_fees""#), "{tools}");
}

/// An address nothing registered is a refusal, and a method the address does
/// not answer is a different one.
#[test]
fn an_unknown_address_is_refused() {
    let (http, _) = doors();
    assert_eq!(over_http(&http, "/nope").0, 404);
}
