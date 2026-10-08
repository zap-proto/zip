// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! A streaming service: one JSON op, one event stream, one byte relay — the
//! three shapes a chat-completions router is made of.

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use futures_util::stream;
use serde::{Deserialize, Serialize};
use zip::{Body, Cx, Error, Event, Sse, Wire};

// The projections, as zipc wrote them from what this crate declared.
include!(concat!(env!("OUT_DIR"), "/documents.rs"));

/// Text is a line of text.
#[derive(Wire, Serialize, Deserialize, Default)]
#[serde(default)]
pub struct Text {
    /// Text is the line.
    #[zip(required)]
    pub text: String,
}

/// Count asks for a stream of ticks.
#[derive(Wire, Serialize, Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
pub struct Count {
    /// N is how many ticks to send.
    #[zip(required)]
    pub n: u32,
    /// EveryMs is the pause before each tick, in milliseconds.
    pub every_ms: u64,
}

/// Tick is one event of a count.
#[derive(Serialize, Deserialize)]
pub struct Tick {
    /// I is the tick's number, from 1.
    pub i: u32,
}

/// Chat is the service. `open` is how many streams are being produced right
/// now: a gauge a deployment exports, and what shows a stream was dropped.
#[derive(Default)]
pub struct Chat {
    pub open: Arc<AtomicUsize>,
}

#[zip::ops(
    app = "stream",
    title = "Streaming",
    version = "1.0.0",
    description = "One JSON op, one event stream and one byte relay."
)]
impl Chat {
    /// Echo answers the text it was sent.
    ///
    /// Example: {"text": "hi"}
    /// Response: {"text": "hi"}
    #[post("/v1/echo")]
    async fn echo(&self, arg: &Text) -> Result<Text, Error> {
        Ok(Text {
            text: arg.text.clone(),
        })
    }

    /// Count streams n ticks as server-sent events, then [DONE], and reports how
    /// many it sent in the x-sent trailer.
    ///
    /// Example: {"n": 3, "everyMs": 100}
    #[post("/v1/count")]
    async fn count(&self, cx: &Cx, arg: &Count) -> Result<Sse, Error> {
        let sent = cx.trailer("x-sent")?;
        let open = Open::new(&self.open);
        let (n, every) = (arg.n, Duration::from_millis(arg.every_ms));
        // The producer's state is the stream's: dropping the stream — which is
        // what the door does when the client leaves — drops `open` with it.
        let ticks = stream::unfold((1, open), move |(i, open)| {
            let sent = sent.clone();
            async move {
                if i > n + 1 {
                    return None;
                }
                if i == n + 1 {
                    sent.set(&n.to_string()).ok()?;
                    return Some((Ok::<_, Error>(Event::data("[DONE]")), (i + 1, open)));
                }
                tokio::time::sleep(every).await;
                Some((Event::json(&Tick { i }), (i + 1, open)))
            }
        });
        Ok(Sse::new(ticks).keep(Duration::from_secs(15)))
    }

    /// Relay answers the body it was sent, each chunk as it arrives, under the
    /// content type it was sent with.
    #[post("/v1/relay")]
    async fn relay(&self, cx: &Cx, body: Body) -> Result<Body, Error> {
        if let Some(kind) = cx.header("content-type") {
            cx.set_header("content-type", kind)?;
        }
        Ok(body)
    }
}

/// Open counts one stream for as long as it lives.
struct Open(Arc<AtomicUsize>);

impl Open {
    fn new(gauge: &Arc<AtomicUsize>) -> Open {
        gauge.fetch_add(1, Ordering::SeqCst);
        Open(Arc::clone(gauge))
    }
}

impl Drop for Open {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::SeqCst);
    }
}

/// service is the app, with its documents on it.
pub fn service(chat: Chat) -> zip::App {
    let mut app = chat.ops();
    if !OPENAPI.is_empty() {
        app.serve("/openapi.json", "application/json", OPENAPI);
    }
    if !MCP.is_empty() {
        app.serve("/mcp.json", "application/json", MCP);
    }
    app
}
