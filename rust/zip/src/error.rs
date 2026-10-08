// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! What an op answers when it cannot.
//!
//! A status and a sentence. The status is the contract — a client branches on
//! it — and the sentence is for the person reading the log, so it says what
//! happened and never what it was holding when it happened.

use std::fmt;

/// Error is a refusal with a status on it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Error {
    pub status: u16,
    /// detail says what happened to THIS request. The title is the status's own
    /// phrase and says what kind of thing happened, which is what a client
    /// branches on; the detail is for the person reading it.
    pub detail: String,
}

impl Error {
    /// new is a refusal at a status of your choosing.
    pub fn new(status: u16, detail: impl Into<String>) -> Self {
        Error {
            status,
            detail: detail.into(),
        }
    }
    /// bad is a request this service will not read: 400.
    pub fn bad(detail: impl Into<String>) -> Self {
        Error::new(400, detail)
    }
    /// missing is an address that names nothing: 404.
    pub fn missing(detail: impl Into<String>) -> Self {
        Error::new(404, detail)
    }
    /// broke is this service's own fault: 500.
    pub fn broke(detail: impl Into<String>) -> Self {
        Error::new(500, detail)
    }

    /// problem is the body a refusal travels in — RFC 9457, the same shape the
    /// Go side answers with, so one client reads both. Its members are written
    /// in name order, which is the order Go's encoder writes a map in.
    pub fn problem(&self) -> String {
        format!(
            "{{\"detail\":{},\"status\":{},\"title\":{},\"type\":\"about:blank\"}}",
            quote(&self.detail),
            self.status,
            quote(reason(self.status)),
        )
    }
}

fn quote(s: &str) -> String {
    serde_json::to_string(s).expect("a string always encodes")
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}: {}", self.status, self.detail)
    }
}

impl std::error::Error for Error {}

/// reason is the phrase beside a status.
pub fn reason(status: u16) -> &'static str {
    hyper::StatusCode::from_u16(status)
        .ok()
        .and_then(|s| s.canonical_reason())
        .unwrap_or("Unknown")
}
