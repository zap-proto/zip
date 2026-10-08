// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! zip — a ZAP-native web framework, in Rust.
//!
//! A handler is a method with a doc comment and a route on it. Registering it
//! yields the REST route, the ZAP method, the OpenAPI document, the MCP tool,
//! the CLI command and the ZAP IDL, from that one registration and the sentence
//! above it. Nothing is declared twice.
//!
//! ```ignore
//! #[zip::ops(app = "chat", title = "Chat", version = "1.0.0")]
//! impl Chat {
//!     /// Echo answers the text it was sent.
//!     ///
//!     /// Example: {"text": "hi"}
//!     /// Response: {"text": "hi"}
//!     #[post("/v1/echo")]
//!     async fn echo(&self, arg: &Ping) -> Result<Pong, zip::Error> {
//!         Ok(Pong { text: arg.text.clone() })
//!     }
//!
//!     /// Complete streams a completion, one event a token.
//!     #[post("/v1/complete")]
//!     async fn complete(&self, cx: &zip::Cx, arg: &Prompt) -> Result<zip::Sse, zip::Error> {
//!         let tokens = cx.trailer("x-tokens")?;
//!         Ok(zip::Sse::new(self.model.stream(arg, tokens)).keep(Duration::from_secs(15)))
//!     }
//! }
//! ```
//!
//! An op is an `async fn` taking `&self`, optionally `&zip::Cx` (the request's
//! headers, and the answer's status, headers and trailers), and at most one
//! input: a type deriving [Wire] and serde's `Deserialize`, bound from the body,
//! the query, the path and the headers it names, or a [Body], the request body
//! as it arrives. It answers `Result<T, zip::Error>` where T is a type deriving
//! [Wire] and `Serialize` (one JSON value), `()` (204), [Sse] (an event stream)
//! or [Body] (a byte stream).
//!
//! # Where the documents come from
//!
//! The macro writes what it read — the ops, the types, the prose — as the crate
//! compiles. `zipc` projects that description into the OpenAPI document, the MCP
//! tool list, the CLI and the .zap schema. A streamed answer is part of what it
//! read: the manifest says `"stream": "sse"` or `"bytes"`, and the document
//! publishes `text/event-stream` or `application/octet-stream` for it.
//!
//! The projector is one program for all three languages, which is the whole
//! point: an operation carries one id, one summary and one schema whether it was
//! written in Rust, in Go or in C++, because one piece of code derived all
//! three. What is native is DECLARING an op and RUNNING it, and both of those
//! are Rust here, all the way down. No Go runs in a service built with this.

mod app;
mod bind;
mod body;
mod cx;
mod desc;
mod error;

pub mod base64;
pub mod http;
pub mod zap;
pub mod zaphttp;

pub use app::{App, Op};
pub use bind::{bind, value, MAX_BODY};
pub use body::{Body, Event, Reply, Sse};
pub use cx::{Cx, Trailer};
pub use desc::{FieldDesc, Scalar, TypeDesc, Wire};
pub use error::Error;

/// The two macros. `ops` declares a service; `Wire` makes a type say what it is.
pub use zip_macros::{ops, Wire};

/// serde is the serde zip reads and writes with, so generated code names one
/// serde whatever a service depends on.
pub use serde;

/// manifest is this service's whole description, as one document.
///
/// The ops half comes from the macro that read the impl block; the types half
/// is every type those ops reach, each stating itself. Nothing here decides
/// anything — it is a join — so the document a service prints is the
/// declaration it compiled from and cannot be anything else.
///
/// `zipc` reads it and writes the OpenAPI document, the MCP tool list, the CLI
/// and the ZAP schema. A Go service's manifest is the same document, filled in
/// the same way by the front end that language has.
pub fn manifest(ops: &str, types: &[(&str, &str)]) -> String {
    let mut sorted: Vec<&(&str, &str)> = types.iter().collect();
    sorted.sort_by_key(|(id, _)| *id);
    let mut out = String::from("{");
    out.push_str(ops);
    out.push_str(",\"types\":[");
    for (i, (_, stated)) in sorted.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        out.push_str(stated);
    }
    out.push_str("]}");
    out
}

/// listen serves an app on every address given until `shutdown` resolves,
/// then drains both doors for at most `grace` and returns.
///
/// One verb, whatever the transport: an address decides which door it is, so a
/// binary states all of its listeners the same way and a deployment changes one
/// without touching the service.
///
/// ```text
/// info --zap :9653 --http :8000
/// ```
pub async fn listen<F>(
    app: App,
    http: Option<&str>,
    zap: Option<&str>,
    shutdown: F,
    grace: std::time::Duration,
) -> std::io::Result<()>
where
    F: std::future::Future<Output = ()> + Send,
{
    let app = std::sync::Arc::new(app);
    let (stop, stopped) = tokio::sync::watch::channel(false);
    let mut doors = tokio::task::JoinSet::new();
    let door = |mut stopped: tokio::sync::watch::Receiver<bool>| async move {
        let _ = stopped.wait_for(|s| *s).await;
    };
    if let Some(addr) = zap {
        let listener = tokio::net::TcpListener::bind(addr).await?;
        let app = std::sync::Arc::clone(&app);
        let stopped = door(stopped.clone());
        doors.spawn(async move { zaphttp::serve(app, listener, stopped, grace).await });
    }
    if let Some(addr) = http {
        let listener = tokio::net::TcpListener::bind(addr).await?;
        let app = std::sync::Arc::clone(&app);
        let stopped = door(stopped.clone());
        doors.spawn(async move { http::serve(app, listener, stopped, grace).await });
    }
    shutdown.await;
    let _ = stop.send(true);
    while let Some(done) = doors.join_next().await {
        done.map_err(std::io::Error::other)??;
    }
    Ok(())
}

/// signal resolves when the process is asked to stop: SIGINT or SIGTERM, the
/// second being what a pod is sent.
pub async fn signal() {
    let interrupt = tokio::signal::ctrl_c();
    #[cfg(unix)]
    {
        let mut term =
            match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
                Ok(t) => t,
                Err(_) => {
                    let _ = interrupt.await;
                    return;
                }
            };
        tokio::select! {
            _ = interrupt => {}
            _ = term.recv() => {}
        }
    }
    #[cfg(not(unix))]
    {
        let _ = interrupt.await;
    }
}
