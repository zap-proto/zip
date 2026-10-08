// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The app: a typed-op registry, and the one path from a request to an answer.
//!
//! An op is registered once. What answers a REST request and what answers a ZAP
//! frame are the same future, reached the same way — route, bind, run, answer —
//! so a browser and a sibling service are talking to one handler and not to two
//! spellings of one.

use std::collections::HashMap;
use std::future::Future;
use std::net::SocketAddr;
use std::panic::AssertUnwindSafe;
use std::sync::{Arc, Mutex};

use bytes::Bytes;
use futures_util::future::BoxFuture;
use futures_util::stream::BoxStream;
use futures_util::FutureExt;
use hyper::header::{self, HeaderMap, HeaderValue};

use crate::cx::{lock, Out};
use crate::{Body, Cx, Error, Reply};

type Call = Box<dyn Fn(Cx, Body) -> BoxFuture<'static, Result<Reply, Error>> + Send + Sync>;

/// Op is one registered operation.
pub struct Op {
    pub method: &'static str,
    pub path: &'static str,
    call: Call,
    segments: Vec<Segment>,
}

enum Segment {
    Literal(String),
    Param(String),
    Rest(String),
}

/// App is a service: its ops, and what it says about itself.
pub struct App {
    pub name: &'static str,
    pub title: &'static str,
    pub version: &'static str,
    pub description: &'static str,
    ops: Vec<Op>,
    files: HashMap<String, (&'static str, &'static str)>,
}

/// Asked is one request as a door read it.
pub(crate) struct Asked {
    pub method: String,
    pub target: String,
    pub headers: HeaderMap,
    pub peer: Option<SocketAddr>,
    pub body: Body,
}

/// Answer is what a door writes back: a head, then a body that is either in
/// hand or still being produced, then — when the op declared any — trailers.
pub(crate) struct Answer {
    pub status: u16,
    pub headers: HeaderMap,
    pub payload: Payload,
    /// trailers is the handler's state when it declared a trailer, read once
    /// the payload has ended.
    pub trailers: Option<Arc<Mutex<Out>>>,
}

pub(crate) enum Payload {
    Full(Bytes),
    Stream(BoxStream<'static, Result<Bytes, Error>>),
}

impl Answer {
    pub(crate) fn refusal(e: &Error, headers: HeaderMap) -> Answer {
        let mut headers = headers;
        headers.insert(
            header::CONTENT_TYPE,
            HeaderValue::from_static("application/problem+json"),
        );
        Answer {
            status: e.status,
            headers,
            payload: Payload::Full(Bytes::from(e.problem())),
            trailers: None,
        }
    }
}

/// trailers is what the handler set by the time the body ended, or None when
/// it set nothing.
pub(crate) fn trailers(out: &Mutex<Out>) -> Option<HeaderMap> {
    let t = std::mem::take(&mut lock(out).trailers);
    (!t.is_empty()).then_some(t)
}

impl App {
    pub fn new(
        name: &'static str,
        title: &'static str,
        version: &'static str,
        description: &'static str,
    ) -> Self {
        App {
            name,
            title,
            version,
            description,
            ops: Vec::new(),
            files: HashMap::new(),
        }
    }

    /// op registers one operation. The macro calls this; a service does not.
    pub fn op<F, R>(&mut self, method: &'static str, path: &'static str, call: F) -> &mut Self
    where
        F: Fn(Cx, Body) -> R + Send + Sync + 'static,
        R: Future<Output = Result<Reply, Error>> + Send + 'static,
    {
        self.ops.push(Op {
            method,
            path,
            call: Box::new(move |cx, body| Box::pin(call(cx, body))),
            segments: split(path),
        });
        self
    }

    /// serve publishes a file at an address: the projections zipc wrote, so a
    /// caller can read the document from the service that implements it.
    pub fn serve(&mut self, path: &str, kind: &'static str, body: &'static str) -> &mut Self {
        self.files.insert(path.to_string(), (kind, body));
        self
    }

    pub fn ops_len(&self) -> usize {
        self.ops.len()
    }

    /// routes is every registered address, for a startup line.
    pub fn routes(&self) -> Vec<(&'static str, &'static str)> {
        self.ops.iter().map(|o| (o.method, o.path)).collect()
    }

    /// answer runs a request: route, bind, run, answer. It is the ONE path in,
    /// so a REST request and a ZAP frame cannot diverge.
    pub(crate) async fn answer(&self, asked: Asked) -> Answer {
        let (path, query) = split_target(&asked.target);
        if asked.method == "GET" {
            if let Some((kind, text)) = self.files.get(path) {
                let mut headers = HeaderMap::new();
                headers.insert(header::CONTENT_TYPE, HeaderValue::from_static(kind));
                return Answer {
                    status: 200,
                    headers,
                    payload: Payload::Full(Bytes::from_static(text.as_bytes())),
                    trailers: None,
                };
            }
        }
        let mut allowed = false;
        for op in &self.ops {
            let Some(params) = op.bind_path(path) else {
                continue;
            };
            if op.method != asked.method {
                allowed = true;
                continue;
            }
            let cx = Cx::new(
                asked.method,
                path.to_string(),
                query,
                params,
                asked.headers,
                asked.peer,
            );
            let out = Arc::clone(&cx.out);
            let run = AssertUnwindSafe((op.call)(cx, asked.body)).catch_unwind();
            let reply = match run.await {
                Ok(r) => r,
                Err(_) => Err(Error::broke("the handler panicked")),
            };
            return reply_to(reply, out);
        }
        let e = if allowed {
            Error::new(405, "that address does not answer this method")
        } else {
            Error::missing("no such address")
        };
        Answer::refusal(&e, HeaderMap::new())
    }
}

/// reply_to is a reply as a head and a body: the kind's own status and content
/// type, then whatever the handler set on its Cx over them.
fn reply_to(reply: Result<Reply, Error>, out: Arc<Mutex<Out>>) -> Answer {
    let (status, mut headers, declared) = {
        let mut o = lock(&out);
        (
            o.status.take(),
            std::mem::take(&mut o.headers),
            std::mem::take(&mut o.declared),
        )
    };
    let reply = match reply {
        Ok(r) => r,
        Err(e) => return Answer::refusal(&e, headers),
    };
    let (dflt, kind, payload) = match reply {
        Reply::Json(b) => (200, Some("application/json"), Payload::Full(b)),
        Reply::Empty => (204, None, Payload::Full(Bytes::new())),
        Reply::Sse(s) => {
            headers
                .entry(header::CACHE_CONTROL)
                .or_insert(HeaderValue::from_static("no-cache"));
            (200, Some("text/event-stream"), Payload::Stream(s.bytes()))
        }
        Reply::Body(b) => (
            200,
            Some("application/octet-stream"),
            Payload::Stream(Box::pin(b)),
        ),
    };
    if let Some(kind) = kind {
        headers
            .entry(header::CONTENT_TYPE)
            .or_insert(HeaderValue::from_static(kind));
    }
    let trailers = if declared.is_empty() {
        None
    } else {
        let names: Vec<&str> = declared.iter().map(|n| n.as_str()).collect();
        let value = HeaderValue::from_str(&names.join(", "))
            .expect("header names joined by commas are a header value");
        headers.insert(header::TRAILER, value);
        Some(out)
    };
    Answer {
        status: status.unwrap_or(dflt),
        headers,
        payload,
        trailers,
    }
}

impl Op {
    /// bind_path matches this op's pattern against an address, answering the
    /// segments it captured.
    fn bind_path(&self, path: &str) -> Option<Vec<(String, String)>> {
        let mut got = Vec::new();
        let mut parts = path.split('/').filter(|s| !s.is_empty());
        let mut want = self.segments.iter();
        loop {
            match want.next() {
                None => {
                    return if parts.next().is_none() {
                        Some(got)
                    } else {
                        None
                    }
                }
                Some(Segment::Rest(name)) => {
                    let rest: Vec<&str> = parts.collect();
                    got.push((name.clone(), rest.join("/")));
                    return Some(got);
                }
                Some(Segment::Literal(lit)) => {
                    if parts.next()? != lit {
                        return None;
                    }
                }
                Some(Segment::Param(name)) => {
                    got.push((name.clone(), parts.next()?.to_string()));
                }
            }
        }
    }
}

fn split(path: &str) -> Vec<Segment> {
    path.split('/')
        .filter(|s| !s.is_empty())
        .map(|s| {
            if let Some(name) = s.strip_prefix(':') {
                Segment::Param(name.to_string())
            } else if s == "*" || s == "+" {
                Segment::Rest("*1".to_string())
            } else {
                Segment::Literal(s.to_string())
            }
        })
        .collect()
}

fn split_target(target: &str) -> (&str, Vec<(String, String)>) {
    let (path, rest) = match target.split_once('?') {
        Some((p, q)) => (p, q),
        None => (target, ""),
    };
    let mut query = Vec::new();
    for pair in rest.split('&').filter(|s| !s.is_empty()) {
        let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
        query.push((unescape(k), unescape(v)));
    }
    (path, query)
}

/// unescape reads percent-encoding out of one URL value.
fn unescape(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'+' => {
                out.push(b' ');
                i += 1;
            }
            b'%' if i + 2 < b.len() => {
                match u8::from_str_radix(std::str::from_utf8(&b[i + 1..i + 3]).unwrap_or("zz"), 16)
                {
                    Ok(v) => {
                        out.push(v);
                        i += 3;
                    }
                    Err(_) => {
                        out.push(b'%');
                        i += 1;
                    }
                }
            }
            c => {
                out.push(c);
                i += 1;
            }
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn op(path: &'static str) -> Op {
        Op {
            method: "GET",
            path,
            call: Box::new(|_, _| Box::pin(async { Ok(Reply::Empty) })),
            segments: split(path),
        }
    }

    #[test]
    fn a_pattern_binds_what_it_names() {
        let o = op("/v1/models/:id");
        assert_eq!(
            o.bind_path("/v1/models/kai"),
            Some(vec![("id".to_string(), "kai".to_string())])
        );
        assert_eq!(o.bind_path("/v1/models"), None);
        assert_eq!(o.bind_path("/v1/models/kai/x"), None);
        let rest = op("/files/*");
        assert_eq!(
            rest.bind_path("/files/a/b"),
            Some(vec![("*1".to_string(), "a/b".to_string())])
        );
    }

    #[test]
    fn a_query_is_unescaped() {
        let (path, q) = split_target("/x?a=1%202&b=c+d&e");
        assert_eq!(path, "/x");
        assert_eq!(
            q,
            vec![
                ("a".to_string(), "1 2".to_string()),
                ("b".to_string(), "c d".to_string()),
                ("e".to_string(), String::new()),
            ]
        );
    }
}
