// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The HTTP door: hyper on tokio.
//!
//! HTTP/1.1 with keep-alive, and HTTP/2 over cleartext (prior knowledge) on the
//! same port: the connection's first bytes say which, so a client never has to
//! be told. A request is answered on its connection's task, and its body goes
//! out as the op produces it — each event of a stream is written and flushed
//! when it is yielded, not when a buffer fills.
//!
//! When a client goes away mid-answer, hyper drops the answer's body, and the
//! op's stream with it: over HTTP/1.1 on the read that sees the connection
//! close, over HTTP/2 on the stream's reset.

use std::convert::Infallible;
use std::future::Future;
use std::net::SocketAddr;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::time::Duration;

use bytes::Bytes;
use futures_util::StreamExt;
use http_body_util::BodyExt;
use hyper::body::{Frame, Incoming, SizeHint};
use hyper::service::service_fn;
use hyper_util::rt::{TokioExecutor, TokioIo, TokioTimer};
use hyper_util::server::conn::auto;
use hyper_util::server::graceful::GracefulShutdown;
use tokio::net::TcpListener;
use tokio::sync::watch;

use crate::app::{trailers, Answer, Asked, Payload};
use crate::cx::Out;
use crate::{App, Body, Error};

/// serve answers HTTP on listener until `shutdown` resolves, then stops
/// accepting, lets every connection finish what it is answering, and returns
/// once they have — or once `grace` has passed, dropping what is left.
pub async fn serve<F>(
    app: Arc<App>,
    listener: TcpListener,
    shutdown: F,
    grace: Duration,
) -> std::io::Result<()>
where
    F: Future<Output = ()> + Send,
{
    let mut builder = auto::Builder::new(TokioExecutor::new());
    // A timer turns on hyper's header read timeout, which is what keeps a
    // client that opens a connection and never finishes a request from
    // holding it forever.
    builder.http1().timer(TokioTimer::new());
    builder.http2().timer(TokioTimer::new());
    let graceful = GracefulShutdown::new();
    // kill ends every connection still open when the grace runs out: its task
    // drops it, and with it whatever answer it was still producing.
    let (kill, killed) = watch::channel(false);
    tokio::pin!(shutdown);
    loop {
        let (stream, peer) = tokio::select! {
            conn = listener.accept() => match conn {
                Ok(c) => c,
                // Out of descriptors, or a connection reset before it was
                // taken: the listener is fine, and the next accept may not be.
                Err(_) => {
                    tokio::time::sleep(Duration::from_millis(10)).await;
                    continue;
                }
            },
            () = &mut shutdown => break,
        };
        let _ = stream.set_nodelay(true);
        let app = Arc::clone(&app);
        let svc = service_fn(move |req| respond(Arc::clone(&app), peer, req));
        let conn = builder
            .serve_connection(TokioIo::new(stream), svc)
            .into_owned();
        let conn = graceful.watch(conn);
        let mut killed = killed.clone();
        tokio::spawn(async move {
            tokio::select! {
                _ = conn => {}
                _ = killed.wait_for(|k| *k) => {}
            }
        });
    }
    drop(listener);
    tokio::select! {
        () = graceful.shutdown() => {}
        () = tokio::time::sleep(grace) => {
            let _ = kill.send(true);
        }
    }
    Ok(())
}

async fn respond(
    app: Arc<App>,
    peer: SocketAddr,
    req: hyper::Request<Incoming>,
) -> Result<hyper::Response<Flow>, Infallible> {
    let (parts, incoming) = req.into_parts();
    let target = parts
        .uri
        .path_and_query()
        .map(|p| p.as_str())
        .unwrap_or("/")
        .to_string();
    let body = Body::new(
        incoming
            .into_data_stream()
            .map(|c| c.map_err(|e| Error::bad(format!("the request body broke: {e}")))),
    );
    let answer = app
        .answer(Asked {
            method: parts.method.as_str().to_string(),
            target,
            headers: parts.headers,
            peer: Some(peer),
            body,
        })
        .await;
    Ok(response(answer))
}

fn response(answer: Answer) -> hyper::Response<Flow> {
    let status = hyper::StatusCode::from_u16(answer.status)
        .unwrap_or(hyper::StatusCode::INTERNAL_SERVER_ERROR);
    let mut res = hyper::Response::new(Flow {
        payload: Some(answer.payload),
        trailers: answer.trailers,
    });
    *res.status_mut() = status;
    *res.headers_mut() = answer.headers;
    res
}

/// Flow is an answer's body as hyper polls it: the bytes, then the trailers.
pub(crate) struct Flow {
    payload: Option<Payload>,
    trailers: Option<Arc<Mutex<Out>>>,
}

impl Flow {
    /// end is the frame after the last chunk: the trailers, if the op set any.
    fn end(&mut self) -> Option<Result<Frame<Bytes>, Error>> {
        self.payload = None;
        let set = trailers(self.trailers.take()?.as_ref())?;
        Some(Ok(Frame::trailers(set)))
    }
}

impl hyper::body::Body for Flow {
    type Data = Bytes;
    type Error = Error;

    fn poll_frame(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
    ) -> Poll<Option<Result<Frame<Bytes>, Error>>> {
        let this = &mut *self;
        match &mut this.payload {
            None => Poll::Ready(None),
            Some(Payload::Full(b)) => {
                if !b.is_empty() {
                    return Poll::Ready(Some(Ok(Frame::data(std::mem::take(b)))));
                }
                Poll::Ready(this.end())
            }
            Some(Payload::Stream(s)) => match s.poll_next_unpin(cx) {
                Poll::Pending => Poll::Pending,
                Poll::Ready(Some(Ok(b))) => Poll::Ready(Some(Ok(Frame::data(b)))),
                Poll::Ready(Some(Err(e))) => {
                    this.payload = None;
                    this.trailers = None;
                    Poll::Ready(Some(Err(e)))
                }
                Poll::Ready(None) => Poll::Ready(this.end()),
            },
        }
    }

    fn is_end_stream(&self) -> bool {
        self.payload.is_none() && self.trailers.is_none()
    }

    /// size_hint is exact for a body in hand, so it goes out with a length —
    /// unless the op declared a trailer, which HTTP/1.1 carries only after a
    /// chunked body.
    fn size_hint(&self) -> SizeHint {
        match &self.payload {
            None => SizeHint::with_exact(0),
            Some(Payload::Full(b)) if self.trailers.is_none() => {
                SizeHint::with_exact(b.len() as u64)
            }
            _ => SizeHint::default(),
        }
    }
}
