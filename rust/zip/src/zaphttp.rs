// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The ZAP door.
//!
//! The same requests, carried as ZAP frames instead of as text (see [crate::zap]
//! for the frames). A connection carries one exchange at a time: a request
//! frame in, then either one response frame or — for a streamed answer — a
//! head, a data frame per chunk as the op produces it, and an end frame with
//! the trailers. That is zap-proto/http's own shape, so a Go client reads a
//! Rust stream chunk by chunk, and a Go server's stream reads here the same way.

use std::future::Future;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use bytes::Bytes;
use futures_util::StreamExt;
use hyper::header::{self, HeaderMap, HeaderName, HeaderValue};
use tokio::io::{AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::{mpsc, watch};

use crate::app::{trailers, Answer, Asked, Payload};
use crate::{zap, App, Body, Error};

/// MAX_FRAME is the largest frame this door reads. A length prefix is a
/// promise from whoever sent it, and a door that believes one has handed the
/// peer a way to ask for all the memory there is.
pub const MAX_FRAME: usize = 32 << 20;

/// serve answers ZAP on listener until `shutdown` resolves, then stops
/// accepting; a connection finishes the exchange it is in and closes. It
/// returns when every connection has, or once `grace` has passed, ending
/// those still open.
pub async fn serve<F>(
    app: Arc<App>,
    listener: TcpListener,
    shutdown: F,
    grace: Duration,
) -> std::io::Result<()>
where
    F: Future<Output = ()> + Send,
{
    let (stop, stopped) = watch::channel(false);
    // kill ends every connection still open when the grace runs out.
    let (kill, killed) = watch::channel(false);
    // Every connection holds a sender; the receiver ends when the last one has
    // gone, which is how the drain knows it is done without counting.
    let (live, mut gone) = mpsc::channel::<()>(1);
    tokio::pin!(shutdown);
    loop {
        let (stream, peer) = tokio::select! {
            conn = listener.accept() => match conn {
                Ok(c) => c,
                Err(_) => {
                    tokio::time::sleep(Duration::from_millis(10)).await;
                    continue;
                }
            },
            () = &mut shutdown => break,
        };
        let _ = stream.set_nodelay(true);
        let app = Arc::clone(&app);
        let stopped = stopped.clone();
        let mut killed = killed.clone();
        let live = live.clone();
        tokio::spawn(async move {
            tokio::select! {
                _ = connection(&app, stream, peer, stopped) => {}
                _ = killed.wait_for(|k| *k) => {}
            }
            drop(live);
        });
    }
    drop(listener);
    let _ = stop.send(true);
    drop(live);
    if tokio::time::timeout(grace, gone.recv()).await.is_err() {
        let _ = kill.send(true);
    }
    Ok(())
}

async fn connection(
    app: &App,
    stream: TcpStream,
    peer: SocketAddr,
    mut stopped: watch::Receiver<bool>,
) -> std::io::Result<()> {
    let (read, mut write) = stream.into_split();
    let mut read = BufReader::new(read);
    loop {
        let frame = tokio::select! {
            f = read_frame(&mut read) => f?,
            _ = stopped.wait_for(|s| *s) => return Ok(()),
        };
        let Some(frame) = frame else { return Ok(()) };
        let answer = match zap::Request::wrap(&frame) {
            Some(r) => {
                let mut headers = HeaderMap::new();
                for (name, value) in zap::pairs(r.headers()) {
                    if let (Ok(n), Ok(v)) =
                        (HeaderName::from_bytes(name), HeaderValue::from_bytes(value))
                    {
                        headers.append(n, v);
                    }
                }
                let body = Body::full(Bytes::copy_from_slice(r.body()));
                app.answer(Asked {
                    method: r.method().to_string(),
                    target: r.target().to_string(),
                    headers,
                    peer: Some(peer),
                    body,
                })
                .await
            }
            None => Answer::refusal(&Error::bad("malformed request frame"), HeaderMap::new()),
        };
        write_answer(&mut write, answer).await?;
    }
}

/// write_answer writes one answer: one frame for a body in hand, a head, data
/// frames and an end for a stream. A stream that fails ends the connection
/// without an end frame, which is what tells the peer the answer is cut.
async fn write_answer<W: AsyncWriteExt + Unpin>(w: &mut W, answer: Answer) -> std::io::Result<()> {
    // Trailer is the frame's own slot on this wire, never a header.
    let headers = block(&answer.headers, true);
    match answer.payload {
        Payload::Full(ref body) => {
            let t = answer
                .trailers
                .as_deref()
                .and_then(trailers)
                .map(|t| block(&t, false))
                .unwrap_or_default();
            send(w, &zap::response(answer.status, &headers, body, &t)).await
        }
        Payload::Stream(mut chunks) => {
            send(w, &zap::head(answer.status, &headers)).await?;
            while let Some(chunk) = chunks.next().await {
                match chunk {
                    Ok(c) if c.is_empty() => {}
                    Ok(c) => send(w, &zap::data(&c)).await?,
                    Err(e) => return Err(std::io::Error::other(e)),
                }
            }
            let t = answer
                .trailers
                .as_deref()
                .and_then(trailers)
                .map(|t| block(&t, false))
                .unwrap_or_default();
            send(w, &zap::end(&t)).await
        }
    }
}

fn block(h: &HeaderMap, head: bool) -> Vec<u8> {
    zap::block(
        h.iter()
            .filter(|(n, _)| !head || **n != header::TRAILER)
            .map(|(n, v)| (n.as_str().as_bytes(), v.as_bytes())),
    )
}

async fn send<W: AsyncWriteExt + Unpin>(w: &mut W, frame: &[u8]) -> std::io::Result<()> {
    w.write_all(&(frame.len() as u32).to_be_bytes()).await?;
    w.write_all(frame).await?;
    w.flush().await
}

/// read_frame takes one length-prefixed frame off the connection, or None at
/// its end. A frame of no bytes or past MAX_FRAME ends the connection.
pub async fn read_frame<R: AsyncReadExt + Unpin>(r: &mut R) -> std::io::Result<Option<Vec<u8>>> {
    let mut head = [0u8; 4];
    match r.read_exact(&mut head).await {
        Ok(_) => {}
        Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => return Ok(None),
        Err(e) => return Err(e),
    }
    let n = u32::from_be_bytes(head) as usize;
    if n == 0 || n > MAX_FRAME {
        return Ok(None);
    }
    let mut frame = vec![0u8; n];
    r.read_exact(&mut frame).await?;
    Ok(Some(frame))
}
