// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The HTTP door.
//!
//! HTTP/1.1 over a socket, one thread per connection, and nothing else — no
//! executor to choose, no runtime to agree on, and therefore no dependency a
//! service inherits from its framework. A handler is a plain function; how a
//! deployment schedules them is the deployment's business.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;

use crate::{App, Error};

/// listen serves app over HTTP at addr, and does not return.
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

/// serve answers every request on one connection, until the peer stops asking.
fn serve(app: &App, conn: TcpStream) -> std::io::Result<()> {
    let mut out = conn.try_clone()?;
    let mut r = BufReader::new(conn);
    loop {
        let Some((method, target, headers)) = head(&mut r)? else {
            return Ok(());
        };
        let mut body = Vec::new();
        if let Some(n) = length(&headers) {
            body.resize(n, 0);
            r.read_exact(&mut body)?;
        }
        let close = headers
            .iter()
            .any(|(k, v)| k.eq_ignore_ascii_case("connection") && v.eq_ignore_ascii_case("close"));
        let answer = app.answer(&method, &target, headers, body);
        write(&mut out, answer.status, answer.kind, &answer.body, close)?;
        if close {
            return Ok(());
        }
    }
}

/// head reads the request line and the headers, or None at end of stream.
type Head = Option<(String, String, Vec<(String, String)>)>;

fn head(r: &mut BufReader<TcpStream>) -> std::io::Result<Head> {
    let mut line = String::new();
    if r.read_line(&mut line)? == 0 {
        return Ok(None);
    }
    let mut words = line.split_whitespace();
    let (Some(method), Some(target)) = (words.next(), words.next()) else {
        return Ok(None);
    };
    let (method, target) = (method.to_string(), target.to_string());
    let mut headers = Vec::new();
    loop {
        let mut line = String::new();
        if r.read_line(&mut line)? == 0 {
            break;
        }
        let line = line.trim_end();
        if line.is_empty() {
            break;
        }
        if let Some((k, v)) = line.split_once(':') {
            headers.push((k.trim().to_string(), v.trim().to_string()));
        }
    }
    Ok(Some((method, target, headers)))
}

fn length(headers: &[(String, String)]) -> Option<usize> {
    headers
        .iter()
        .find(|(k, _)| k.eq_ignore_ascii_case("content-length"))
        .and_then(|(_, v)| v.parse().ok())
}

fn write(
    out: &mut TcpStream,
    status: u16,
    kind: &str,
    body: &[u8],
    close: bool,
) -> std::io::Result<()> {
    let mut head = format!(
        "HTTP/1.1 {status} {}\r\nContent-Type: {kind}\r\nContent-Length: {}\r\n",
        reason(status),
        body.len()
    );
    head.push_str(if close {
        "Connection: close\r\n\r\n"
    } else {
        "\r\n"
    });
    out.write_all(head.as_bytes())?;
    out.write_all(body)?;
    out.flush()
}

/// reason is the phrase beside a status. Only the ones this framework answers
/// with: a phrase nothing sends is a phrase nobody reads.
pub fn reason(status: u16) -> &'static str {
    match status {
        200 => "OK",
        201 => "Created",
        202 => "Accepted",
        204 => "No Content",
        400 => "Bad Request",
        401 => "Unauthorized",
        403 => "Forbidden",
        404 => "Not Found",
        405 => "Method Not Allowed",
        409 => "Conflict",
        422 => "Unprocessable Content",
        500 => "Internal Server Error",
        503 => "Service Unavailable",
        _ => "OK",
    }
}

/// refuse is what a door writes when it cannot even read the request.
pub fn refuse(e: &Error) -> Vec<u8> {
    e.problem().into_bytes()
}
