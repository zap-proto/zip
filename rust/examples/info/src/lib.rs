// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! A node's info service: fourteen operations, written once.

pub mod ops;
pub mod types;

// The projections, as zipc wrote them from what this crate declared. A service
// publishes the document it implements. Empty on the build that MAKES them,
// which is the one build where they cannot exist yet.
include!(concat!(env!("OUT_DIR"), "/documents.rs"));

/// service is this node's info app, with its documents on it.
pub fn service(release: &str, network: u32) -> zip::App {
    let mut app = ops::Info {
        release: release.to_string(),
        network,
    }
    .ops();
    app.documents(OPENAPI, MCP);
    app
}
