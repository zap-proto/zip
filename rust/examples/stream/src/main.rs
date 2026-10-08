// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The streaming service, serving REST and ZAP from one registry.
//!
//! ```text
//! stream --http 127.0.0.1:8000 --zap 127.0.0.1:9653
//! ```

#[tokio::main]
async fn main() {
    let mut http = Some("127.0.0.1:8000".to_string());
    let mut zap = None;
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
            "--manifest" => {
                println!("{}", stream::Chat::manifest());
                return;
            }
            other => {
                eprintln!("stream: unknown argument {other}");
                std::process::exit(2);
            }
        }
    }
    let app = stream::service(stream::Chat::default());
    for (method, path) in app.routes() {
        println!("  {method} {path}");
    }
    let grace = std::time::Duration::from_secs(25);
    if let Err(e) = zip::listen(app, http.as_deref(), zap.as_deref(), zip::signal(), grace).await {
        eprintln!("stream: {e}");
        std::process::exit(1);
    }
}
