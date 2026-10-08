// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! One request, as a handler sees it beside its input.
//!
//! An op's input is the value the request carried; `Cx` is the rest of the
//! exchange: the headers as they arrived, and what the answer carries besides
//! its body — its status, its headers and its trailers. A handler that takes
//! `&zip::Cx` gets one; most do not need it, since a header the op depends on
//! is better declared on the input (`#[zip(header = "X-Org-Id")]`), where the
//! document can see it.

use std::net::SocketAddr;
use std::sync::{Arc, Mutex, MutexGuard};

use hyper::header::{HeaderMap, HeaderName, HeaderValue};

use crate::Error;

/// Cx is the exchange around an op's input.
pub struct Cx {
    method: String,
    path: String,
    pub(crate) query: Vec<(String, String)>,
    pub(crate) params: Vec<(String, String)>,
    headers: HeaderMap,
    peer: Option<SocketAddr>,
    pub(crate) out: Arc<Mutex<Out>>,
}

/// Out is what the handler said about its answer besides the body.
#[derive(Default)]
pub(crate) struct Out {
    pub status: Option<u16>,
    pub headers: HeaderMap,
    /// declared is every trailer name, in the order declared. Over HTTP/1.1 a
    /// trailer reaches the client only if its name went out in the head.
    pub declared: Vec<HeaderName>,
    pub trailers: HeaderMap,
}

impl Cx {
    pub(crate) fn new(
        method: String,
        path: String,
        query: Vec<(String, String)>,
        params: Vec<(String, String)>,
        headers: HeaderMap,
        peer: Option<SocketAddr>,
    ) -> Cx {
        Cx {
            method,
            path,
            query,
            params,
            headers,
            peer,
            out: Arc::default(),
        }
    }

    pub fn method(&self) -> &str {
        &self.method
    }

    /// path is the address asked for, without its query.
    pub fn path(&self) -> &str {
        &self.path
    }

    /// headers is every request header as it arrived.
    pub fn headers(&self) -> &HeaderMap {
        &self.headers
    }

    /// header is one request header, if it is there and is text.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers.get(name).and_then(|v| v.to_str().ok())
    }

    /// peer is the socket's other end. Behind a gateway it is the gateway.
    pub fn peer(&self) -> Option<SocketAddr> {
        self.peer
    }

    /// set_status answers with this status instead of 200 (or 204 for an op
    /// that answers nothing). A refusal keeps the status it carries.
    pub fn set_status(&self, status: u16) -> Result<(), Error> {
        if !(200..600).contains(&status) {
            return Err(Error::broke(format!("{status} is not a final status")));
        }
        self.out().status = Some(status);
        Ok(())
    }

    /// set_header sets one answer header, replacing any value it had —
    /// including a content type the answer would otherwise carry.
    pub fn set_header(&self, name: &str, value: &str) -> Result<(), Error> {
        let (name, value) = pair(name, value)?;
        self.out().headers.insert(name, value);
        Ok(())
    }

    /// add_header adds one answer header beside any it already has.
    pub fn add_header(&self, name: &str, value: &str) -> Result<(), Error> {
        let (name, value) = pair(name, value)?;
        self.out().headers.append(name, value);
        Ok(())
    }

    /// trailer declares a trailer, and is the handle that sets it.
    ///
    /// The name goes out in the head (`Trailer: name`) and the value after the
    /// last chunk of the body, so a stream can report what it only knows once
    /// it is done — a token count, a digest. Over HTTP/2 a trailer always
    /// arrives. Over HTTP/1.1 it arrives only when the client asked for
    /// trailers (`TE: trailers`), because hyper sends them on no other terms;
    /// a client that did not ask gets the body alone.
    pub fn trailer(&self, name: &str) -> Result<Trailer, Error> {
        let name = HeaderName::from_bytes(name.as_bytes())
            .map_err(|_| Error::broke(format!("{name:?} is not a header name")))?;
        let mut out = self.out();
        if !out.declared.contains(&name) {
            out.declared.push(name.clone());
        }
        Ok(Trailer {
            name,
            out: Arc::clone(&self.out),
        })
    }

    fn out(&self) -> MutexGuard<'_, Out> {
        lock(&self.out)
    }
}

/// Trailer is one declared trailer. It is `Send` and `'static`, so the stream
/// that produces the body can carry it to the end.
#[derive(Clone)]
pub struct Trailer {
    name: HeaderName,
    out: Arc<Mutex<Out>>,
}

impl Trailer {
    /// set gives the trailer its value. A value set after the body ended is
    /// never sent.
    pub fn set(&self, value: &str) -> Result<(), Error> {
        let value = HeaderValue::from_str(value)
            .map_err(|_| Error::broke(format!("{value:?} is not a header value")))?;
        lock(&self.out).trailers.insert(self.name.clone(), value);
        Ok(())
    }
}

/// lock takes the answer's state. Nothing holds it across an await or a call
/// out, so a poisoned lock is one a panic left mid-insert, and the map is still
/// a map.
pub(crate) fn lock(out: &Mutex<Out>) -> MutexGuard<'_, Out> {
    out.lock().unwrap_or_else(|p| p.into_inner())
}

fn pair(name: &str, value: &str) -> Result<(HeaderName, HeaderValue), Error> {
    let n = HeaderName::from_bytes(name.as_bytes())
        .map_err(|_| Error::broke(format!("{name:?} is not a header name")))?;
    let v = HeaderValue::from_str(value)
        .map_err(|_| Error::broke(format!("{value:?} is not a header value")))?;
    Ok((n, v))
}
