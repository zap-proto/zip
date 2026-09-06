// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! A node's info service: fourteen operations, written once.

pub mod ops;
pub mod types;

/// The projections, as zipc wrote them from what the compiler read. A service
/// publishes the document it implements.
pub const OPENAPI: &str = include_str!("../gen/openapi.json");
pub const MCP: &str = include_str!("../gen/mcp.json");

/// service is this node's info app, with its documents on it.
pub fn service(release: &str, network: u32) -> zip::App {
    let mut app = ops::Info {
        release: release.to_string(),
        network,
    }
    .ops();
    app.serve("/openapi.json", "application/json", OPENAPI);
    app.serve("/mcp.json", "application/json", MCP);
    app
}
