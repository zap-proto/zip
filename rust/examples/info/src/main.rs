// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! A node's info service, serving REST and ZAP from one registry.
//!
//! ```text
//! info --http 127.0.0.1:8000 --zap 127.0.0.1:9653
//! ```

fn main() {
    let mut http = Some("127.0.0.1:8000".to_string());
    let mut zap = Some("127.0.0.1:9653".to_string());
    let args: Vec<String> = std::env::args().skip(1).collect();
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--http" => {
                http = args.get(i + 1).cloned();
                i += 2;
            }
            "--zap" => {
                zap = args.get(i + 1).cloned();
                i += 2;
            }
            // What this service declared, for zipc to project. It comes from
            // the binary, so it describes THIS build and cannot be stale.
            "--manifest" => {
                println!("{}", info::ops::Info::manifest());
                return;
            }
            other => {
                eprintln!("info: unknown argument {other}");
                std::process::exit(2);
            }
        }
    }

    let app = info::service("luxd/1.36.178", 96369);
    println!("info: {} ops", app.ops_len());
    for (method, path) in app.routes() {
        println!("  {method} {path}");
    }
    if let Some(a) = &http {
        println!("info: http on {a}");
    }
    if let Some(a) = &zap {
        println!("info: zap on {a}");
    }
    if let Err(e) = zip::listen(app, http.as_deref(), zap.as_deref()) {
        eprintln!("info: {e}");
        std::process::exit(1);
    }
}
