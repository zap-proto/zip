// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! What an op answers, and the bytes it reads as they arrive.
//!
//! An op answers one JSON value, nothing, a stream of server-sent events, or a
//! stream of bytes. The two streams are values the handler returns, not writers
//! it is handed: a door polls them, so a chunk goes out when the producer yields
//! it, and when the peer goes away the door drops the stream — which drops
//! whatever the producer was holding, an upstream request included.
//!
//! A stream item is a `Result`. `Ok` is written; `Err` ends the answer without
//! its terminator — a truncated chunked body over HTTP/1.1, a reset stream over
//! HTTP/2 — because the status went out with the head and is no longer a thing
//! that can say the answer failed. A producer that wants to say so in words
//! sends an event first and then ends.

use std::future::Future;
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::Duration;

use bytes::Bytes;
use futures_util::stream::{BoxStream, Stream, StreamExt};
use serde::Serialize;

use crate::Error;

type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// failed is a stream's error as zip's: a zip::Error keeps its status and
/// detail, anything else is the service's own fault.
fn failed(e: BoxError) -> Error {
    match e.downcast::<Error>() {
        Ok(e) => *e,
        Err(e) => Error::broke(e.to_string()),
    }
}

/// Body is bytes as they arrive: an op's request body, read as sent, or its
/// answer, written as produced.
pub struct Body {
    chunks: BoxStream<'static, Result<Bytes, Error>>,
}

impl Body {
    /// new is a body from any stream of chunks. An error item ends the body
    /// unfinished.
    pub fn new<S, B, E>(chunks: S) -> Body
    where
        S: Stream<Item = Result<B, E>> + Send + 'static,
        B: Into<Bytes>,
        E: Into<BoxError>,
    {
        Body {
            chunks: chunks
                .map(|c| c.map(Into::into).map_err(|e| failed(e.into())))
                .boxed(),
        }
    }

    /// empty is a body with nothing in it.
    pub fn empty() -> Body {
        Body {
            chunks: futures_util::stream::empty().boxed(),
        }
    }

    /// full is a body that is one chunk, already in hand.
    pub fn full(bytes: impl Into<Bytes>) -> Body {
        let bytes = bytes.into();
        if bytes.is_empty() {
            return Body::empty();
        }
        Body {
            chunks: futures_util::stream::once(async move { Ok(bytes) }).boxed(),
        }
    }

    /// bytes reads the whole body, refusing one past `limit` with a 413 as soon
    /// as it is past it — before the rest is read, not after.
    pub async fn bytes(mut self, limit: usize) -> Result<Bytes, Error> {
        let Some(first) = self.chunks.next().await else {
            return Ok(Bytes::new());
        };
        let first = first?;
        let Some(second) = self.chunks.next().await else {
            return within(first, limit);
        };
        let mut all = Vec::with_capacity(first.len() * 2);
        all.extend_from_slice(&first);
        let mut next = Some(second);
        while let Some(chunk) = next {
            let chunk = chunk?;
            if all.len() + chunk.len() > limit {
                return Err(too_large(limit));
            }
            all.extend_from_slice(&chunk);
            next = self.chunks.next().await;
        }
        Ok(Bytes::from(all))
    }
}

fn within(b: Bytes, limit: usize) -> Result<Bytes, Error> {
    if b.len() > limit {
        return Err(too_large(limit));
    }
    Ok(b)
}

fn too_large(limit: usize) -> Error {
    Error::new(413, format!("the body is over {limit} bytes"))
}

impl Stream for Body {
    type Item = Result<Bytes, Error>;
    fn poll_next(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        self.chunks.poll_next_unpin(cx)
    }
}

/// Event is one server-sent event.
///
/// Every field is optional on the wire and each is written as its own line, so
/// a value holding a line break is split into as many lines as it holds and
/// cannot start a field of its own: `data` and comments become several lines
/// of the same field, and `event` and `id`, which are one line by definition,
/// lose their line breaks.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Event {
    data: Option<String>,
    event: Option<String>,
    id: Option<String>,
    retry: Option<u64>,
    comment: Option<String>,
}

impl Event {
    /// data is an event carrying text.
    pub fn data(text: impl Into<String>) -> Event {
        Event {
            data: Some(text.into()),
            ..Event::default()
        }
    }

    /// json is an event carrying one JSON value, on one line.
    pub fn json<T: Serialize>(value: &T) -> Result<Event, Error> {
        serde_json::to_string(value)
            .map(Event::data)
            .map_err(|e| Error::broke(format!("an event did not encode: {e}")))
    }

    /// comment is a line a client ignores. It is what keeps an idle stream
    /// open through a proxy that closes a quiet one.
    pub fn comment(text: impl Into<String>) -> Event {
        Event {
            comment: Some(text.into()),
            ..Event::default()
        }
    }

    /// event names the kind of event, which a browser dispatches on.
    pub fn event(mut self, name: impl Into<String>) -> Event {
        self.event = Some(one_line(name.into()));
        self
    }

    /// id is the id a reconnecting client sends back as Last-Event-ID.
    pub fn id(mut self, id: impl Into<String>) -> Event {
        self.id = Some(one_line(id.into()));
        self
    }

    /// retry is how long, in milliseconds, a client waits before reconnecting.
    pub fn retry(mut self, ms: u64) -> Event {
        self.retry = Some(ms);
        self
    }

    /// encode is this event as the bytes the stream carries.
    pub fn encode(&self) -> Bytes {
        let mut out = String::new();
        if let Some(c) = &self.comment {
            for line in lines(c) {
                out.push(':');
                if !line.is_empty() {
                    out.push(' ');
                    out.push_str(line);
                }
                out.push('\n');
            }
        }
        if let Some(e) = &self.event {
            out.push_str("event: ");
            out.push_str(e);
            out.push('\n');
        }
        if let Some(id) = &self.id {
            out.push_str("id: ");
            out.push_str(id);
            out.push('\n');
        }
        if let Some(ms) = self.retry {
            out.push_str("retry: ");
            out.push_str(&ms.to_string());
            out.push('\n');
        }
        if let Some(d) = &self.data {
            for line in lines(d) {
                out.push_str("data: ");
                out.push_str(line);
                out.push('\n');
            }
        }
        out.push('\n');
        Bytes::from(out)
    }
}

/// lines splits on every line break the event-stream format knows: CRLF, LF
/// and a lone CR.
fn lines(s: &str) -> Vec<&str> {
    let mut out = Vec::new();
    let b = s.as_bytes();
    let mut start = 0;
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'\n' => {
                out.push(&s[start..i]);
                start = i + 1;
            }
            b'\r' => {
                out.push(&s[start..i]);
                if b.get(i + 1) == Some(&b'\n') {
                    i += 1;
                }
                start = i + 1;
            }
            _ => {}
        }
        i += 1;
    }
    out.push(&s[start..]);
    out
}

fn one_line(s: String) -> String {
    if s.contains(['\r', '\n']) {
        s.replace(['\r', '\n'], "")
    } else {
        s
    }
}

/// Sse is an answer of server-sent events (`text/event-stream`), each written
/// and flushed as the producer yields it.
pub struct Sse {
    events: BoxStream<'static, Result<Event, Error>>,
    keep: Option<Duration>,
}

impl Sse {
    /// new is an event stream from any stream of events. An error item ends the
    /// stream unfinished; the stream ending ends the answer cleanly.
    pub fn new<S, E>(events: S) -> Sse
    where
        S: Stream<Item = Result<Event, E>> + Send + 'static,
        E: Into<BoxError>,
    {
        Sse {
            events: events.map(|e| e.map_err(|e| failed(e.into()))).boxed(),
            keep: None,
        }
    }

    /// keep writes a comment line whenever `every` passes with no event, so a
    /// proxy does not close a stream that is only waiting, and a client that
    /// left is found out by the write.
    pub fn keep(mut self, every: Duration) -> Sse {
        self.keep = Some(every);
        self
    }

    /// bytes is the stream as the door writes it.
    pub(crate) fn bytes(self) -> BoxStream<'static, Result<Bytes, Error>> {
        match self.keep {
            None => self.events.map(|e| e.map(|e| e.encode())).boxed(),
            Some(every) => Keep {
                events: self.events,
                every,
                timer: Box::pin(tokio::time::sleep(every)),
            }
            .boxed(),
        }
    }
}

/// Keep is an event stream with a comment line in every quiet interval.
struct Keep {
    events: BoxStream<'static, Result<Event, Error>>,
    every: Duration,
    timer: Pin<Box<tokio::time::Sleep>>,
}

/// KEEP is the line a quiet interval writes: an empty comment.
const KEEP: &[u8] = b":\n\n";

impl Stream for Keep {
    type Item = Result<Bytes, Error>;
    fn poll_next(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let this = &mut *self;
        if let Poll::Ready(next) = this.events.poll_next_unpin(cx) {
            let at = tokio::time::Instant::now() + this.every;
            this.timer.as_mut().reset(at);
            return Poll::Ready(next.map(|e| e.map(|e| e.encode())));
        }
        if this.timer.as_mut().poll(cx).is_ready() {
            let at = tokio::time::Instant::now() + this.every;
            this.timer.as_mut().reset(at);
            return Poll::Ready(Some(Ok(Bytes::from_static(KEEP))));
        }
        Poll::Pending
    }
}

/// Reply is what an op answered. The macro builds it from the handler's return
/// value; a handler names `Sse`, `Body` or a type of its own and never this.
pub enum Reply {
    /// Json is one value, encoded: 200, `application/json`.
    Json(Bytes),
    /// Empty is an op that answers nothing: 204.
    Empty,
    /// Sse is an event stream: 200, `text/event-stream`.
    Sse(Sse),
    /// Body is a byte stream: 200, `application/octet-stream` unless the
    /// handler set a type.
    Body(Body),
}

impl Reply {
    /// json encodes one answer.
    pub fn json<T: Serialize>(value: &T) -> Result<Reply, Error> {
        serde_json::to_vec(value)
            .map(|b| Reply::Json(Bytes::from(b)))
            .map_err(|e| Error::broke(format!("the answer did not encode: {e}")))
    }
}

impl From<Sse> for Reply {
    fn from(s: Sse) -> Reply {
        Reply::Sse(s)
    }
}

impl From<Body> for Reply {
    fn from(b: Body) -> Reply {
        Reply::Body(b)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_event_is_its_fields_then_a_blank_line() {
        assert_eq!(&Event::data("hi").encode()[..], b"data: hi\n\n");
        assert_eq!(
            &Event::data("a\nb\r\nc\rd")
                .event("delta")
                .id("7")
                .retry(500)
                .encode()[..],
            b"event: delta\nid: 7\nretry: 500\ndata: a\ndata: b\ndata: c\ndata: d\n\n"
        );
        assert_eq!(&Event::comment("").encode()[..], b":\n\n");
        assert_eq!(&Event::comment("ping").encode()[..], b": ping\n\n");
        // A line break cannot start a field of its own.
        assert_eq!(
            &Event::data("x").event("a\ndata: forged").encode()[..],
            b"event: adata: forged\ndata: x\n\n"
        );
        let e = Event::json(&serde_json::json!({"a": "b\nc"})).unwrap();
        assert_eq!(&e.encode()[..], b"data: {\"a\":\"b\\nc\"}\n\n");
    }

    #[tokio::test]
    async fn a_body_past_its_limit_is_refused() {
        let chunks = futures_util::stream::iter(vec![
            Ok::<_, Error>(Bytes::from_static(b"12345")),
            Ok(Bytes::from_static(b"678")),
        ]);
        let got = Body::new(chunks).bytes(6).await.unwrap_err();
        assert_eq!(got.status, 413);
        let chunks = futures_util::stream::iter(vec![
            Ok::<_, Error>(Bytes::from_static(b"12345")),
            Ok(Bytes::from_static(b"678")),
        ]);
        assert_eq!(&Body::new(chunks).bytes(8).await.unwrap()[..], b"12345678");
        assert_eq!(
            Body::full("1234567").bytes(6).await.unwrap_err().status,
            413
        );
        assert!(Body::empty().bytes(0).await.unwrap().is_empty());
    }

    #[tokio::test(start_paused = true)]
    async fn a_quiet_stream_keeps_itself_open() {
        let (tx, rx) = tokio::sync::mpsc::channel::<Result<Event, Error>>(1);
        let mut s = Sse::new(futures_util::stream::unfold(rx, |mut rx| async move {
            rx.recv().await.map(|e| (e, rx))
        }))
        .keep(Duration::from_secs(15))
        .bytes();
        // Nothing for 15 s: a comment.
        assert_eq!(&s.next().await.unwrap().unwrap()[..], KEEP);
        tx.send(Ok(Event::data("1"))).await.unwrap();
        assert_eq!(&s.next().await.unwrap().unwrap()[..], b"data: 1\n\n");
        drop(tx);
        assert!(s.next().await.is_none());
    }
}
