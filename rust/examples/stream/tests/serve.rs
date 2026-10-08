// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The service on a real port, asked over real sockets.
//!
//! HTTP/1.1 is spoken by hand here, on a bare TCP stream, so the test sees
//! what the wire carries — the chunks, when each arrived, the trailers after
//! the last — and can hang up mid-answer exactly when it means to.

use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::{Duration, Instant};

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::oneshot;
use tokio::task::JoinHandle;

struct Server {
    addr: String,
    open: Arc<std::sync::atomic::AtomicUsize>,
    stop: Option<oneshot::Sender<()>>,
    done: JoinHandle<std::io::Result<()>>,
}

#[derive(Clone, Copy)]
enum Door {
    Http,
    Zap,
}

async fn start(door: Door) -> Server {
    start_with(door, Duration::from_secs(5)).await
}

async fn start_with(door: Door, grace: Duration) -> Server {
    let chat = stream::Chat::default();
    let open = Arc::clone(&chat.open);
    let app = Arc::new(stream::service(chat));
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap().to_string();
    let (stop, stopped) = oneshot::channel::<()>();
    let shutdown = async move {
        let _ = stopped.await;
    };
    let done = match door {
        Door::Http => tokio::spawn(zip::http::serve(app, listener, shutdown, grace)),
        Door::Zap => tokio::spawn(zip::zaphttp::serve(app, listener, shutdown, grace)),
    };
    Server {
        addr,
        open,
        stop: Some(stop),
        done,
    }
}

/// Wire is one HTTP/1.1 connection, read by hand.
struct Wire {
    conn: TcpStream,
    buf: Vec<u8>,
}

impl Wire {
    async fn dial(addr: &str) -> Wire {
        Wire {
            conn: TcpStream::connect(addr).await.unwrap(),
            buf: Vec::new(),
        }
    }

    async fn send(&mut self, text: &str) {
        self.conn.write_all(text.as_bytes()).await.unwrap();
    }

    async fn post(&mut self, path: &str, body: &str, extra: &str) {
        let req = format!(
            "POST {path} HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: {}\r\n{extra}\r\n{body}",
            body.len()
        );
        self.send(&req).await;
    }

    async fn fill(&mut self) {
        let mut chunk = [0u8; 4096];
        let n = tokio::time::timeout(Duration::from_secs(10), self.conn.read(&mut chunk))
            .await
            .expect("the server said nothing for 10 s")
            .unwrap();
        assert!(n > 0, "the server closed the connection");
        self.buf.extend_from_slice(&chunk[..n]);
    }

    async fn line(&mut self) -> String {
        loop {
            if let Some(i) = self.buf.windows(2).position(|w| w == b"\r\n") {
                let line = String::from_utf8(self.buf[..i].to_vec()).unwrap();
                self.buf.drain(..i + 2);
                return line;
            }
            self.fill().await;
        }
    }

    async fn exact(&mut self, n: usize) -> Vec<u8> {
        while self.buf.len() < n {
            self.fill().await;
        }
        self.buf.drain(..n).collect()
    }

    /// head reads a status line and headers, names lowercased.
    async fn head(&mut self) -> (u16, Vec<(String, String)>) {
        let status = self.line().await;
        let code = status.split_whitespace().nth(1).unwrap().parse().unwrap();
        let mut headers = Vec::new();
        loop {
            let l = self.line().await;
            if l.is_empty() {
                return (code, headers);
            }
            let (k, v) = l.split_once(':').unwrap();
            headers.push((k.trim().to_ascii_lowercase(), v.trim().to_string()));
        }
    }

    /// chunk reads one chunk of a chunked body: Some(bytes), or None at the
    /// last-chunk, after which the trailer lines are read.
    async fn chunk(&mut self) -> Option<Vec<u8>> {
        let size = usize::from_str_radix(self.line().await.trim(), 16).unwrap();
        if size == 0 {
            return None;
        }
        let data = self.exact(size).await;
        assert_eq!(self.line().await, "", "a chunk ends in CRLF");
        Some(data)
    }

    async fn trailers(&mut self) -> Vec<String> {
        let mut out = Vec::new();
        loop {
            let l = self.line().await;
            if l.is_empty() {
                return out;
            }
            out.push(l.to_ascii_lowercase());
        }
    }
}

fn header<'a>(h: &'a [(String, String)], name: &str) -> Option<&'a str> {
    h.iter().find(|(k, _)| k == name).map(|(_, v)| v.as_str())
}

/// (a) A JSON op answers, and the connection stays open for the next request.
#[tokio::test(flavor = "multi_thread")]
async fn a_json_op_round_trips_on_one_connection() {
    let s = start(Door::Http).await;
    let mut w = Wire::dial(&s.addr).await;
    for _ in 0..3 {
        w.post("/v1/echo", r#"{"text":"hi"}"#, "").await;
        let (status, h) = w.head().await;
        assert_eq!(status, 200);
        assert_eq!(header(&h, "content-type"), Some("application/json"));
        let n: usize = header(&h, "content-length").unwrap().parse().unwrap();
        assert_eq!(w.exact(n).await, br#"{"text":"hi"}"#);
    }
    // A field the op cannot run without is refused before the handler runs.
    w.post("/v1/echo", "{}", "").await;
    let (status, h) = w.head().await;
    assert_eq!(status, 400);
    assert_eq!(header(&h, "content-type"), Some("application/problem+json"));
    let n: usize = header(&h, "content-length").unwrap().parse().unwrap();
    let body = String::from_utf8(w.exact(n).await).unwrap();
    assert!(body.contains(r#"field \"text\" is required"#), "{body}");
}

/// events reads an event stream until its last chunk, stamping each event
/// with when its blank line arrived.
async fn events(w: &mut Wire, since: Instant) -> Vec<(String, Duration)> {
    let mut out = Vec::new();
    let mut pending = Vec::new();
    while let Some(chunk) = w.chunk().await {
        pending.extend_from_slice(&chunk);
        while let Some(i) = pending.windows(2).position(|x| x == b"\n\n") {
            let event = String::from_utf8(pending[..i].to_vec()).unwrap();
            pending.drain(..i + 2);
            out.push((event, since.elapsed()));
        }
    }
    assert!(pending.is_empty(), "the stream ended mid-event");
    out
}

/// (b) Each event is written when it is produced. Five ticks 200 ms apart
/// arrive 200 ms apart; a server that buffered them would deliver five at once.
#[tokio::test(flavor = "multi_thread")]
async fn an_event_arrives_when_it_is_produced() {
    let s = start(Door::Http).await;
    let mut w = Wire::dial(&s.addr).await;
    let every = Duration::from_millis(200);
    let since = Instant::now();
    w.post("/v1/count", r#"{"n":5,"everyMs":200}"#, "").await;
    let (status, h) = w.head().await;
    assert_eq!(status, 200);
    assert_eq!(header(&h, "content-type"), Some("text/event-stream"));
    assert_eq!(header(&h, "cache-control"), Some("no-cache"));
    assert_eq!(header(&h, "transfer-encoding"), Some("chunked"));
    let got = events(&mut w, since).await;
    // No TE: trailers, so no trailers: hyper sends them on no other terms.
    assert!(w.trailers().await.is_empty());

    let texts: Vec<&str> = got.iter().map(|(e, _)| e.as_str()).collect();
    assert_eq!(
        texts,
        vec![
            r#"data: {"i":1}"#,
            r#"data: {"i":2}"#,
            r#"data: {"i":3}"#,
            r#"data: {"i":4}"#,
            r#"data: {"i":5}"#,
            "data: [DONE]"
        ]
    );
    let at: Vec<u128> = got.iter().map(|(_, t)| t.as_millis()).collect();
    eprintln!("arrivals (ms): {at:?}");
    for pair in got[..5].windows(2) {
        let gap = pair[1].1 - pair[0].1;
        assert!(
            gap >= every / 2,
            "two ticks arrived {gap:?} apart; they were produced {every:?} apart: {at:?}"
        );
    }
    assert!(
        got[4].1 - got[0].1 >= every * 4 / 2,
        "the first tick waited for the last: {at:?}"
    );

    // The stream ended cleanly — its last-chunk arrived — so the connection
    // is still good for another request.
    w.post("/v1/echo", r#"{"text":"after"}"#, "").await;
    assert_eq!(w.head().await.0, 200);
}

/// (c) A client that hangs up mid-stream drops the producer: the stream's
/// state, and whatever it holds, goes with it.
#[tokio::test(flavor = "multi_thread")]
async fn a_client_leaving_drops_the_producer() {
    let s = start(Door::Http).await;
    let mut w = Wire::dial(&s.addr).await;
    w.post("/v1/count", r#"{"n":100000,"everyMs":10}"#, "")
        .await;
    assert_eq!(w.head().await.0, 200);
    let first = w.chunk().await.unwrap();
    assert_eq!(first, b"data: {\"i\":1}\n\n");
    assert_eq!(
        s.open.load(Ordering::SeqCst),
        1,
        "one stream is being produced"
    );

    let left = Instant::now();
    drop(w);
    while s.open.load(Ordering::SeqCst) != 0 {
        assert!(
            left.elapsed() < Duration::from_secs(5),
            "the producer outlived its client by 5 s"
        );
        tokio::time::sleep(Duration::from_millis(1)).await;
    }
    eprintln!(
        "producer dropped {:?} after the client left",
        left.elapsed()
    );
}

/// The same, while the producer is idle: no event is due for a minute, so
/// nothing is being written, and the hang-up alone has to be noticed.
#[tokio::test(flavor = "multi_thread")]
async fn a_client_leaving_an_idle_stream_drops_the_producer() {
    let s = start(Door::Http).await;
    let mut w = Wire::dial(&s.addr).await;
    w.post("/v1/count", r#"{"n":3,"everyMs":60000}"#, "").await;
    assert_eq!(w.head().await.0, 200);
    while s.open.load(Ordering::SeqCst) != 1 {
        tokio::time::sleep(Duration::from_millis(1)).await;
    }
    let left = Instant::now();
    drop(w);
    while s.open.load(Ordering::SeqCst) != 0 {
        assert!(
            left.elapsed() < Duration::from_secs(5),
            "an idle producer outlived its client by 5 s"
        );
        tokio::time::sleep(Duration::from_millis(1)).await;
    }
    eprintln!(
        "idle producer dropped {:?} after the client left",
        left.elapsed()
    );
}

/// (d) The service publishes the document its declaration projects, and the
/// document says what each op answers with.
#[tokio::test(flavor = "multi_thread")]
async fn the_document_lists_the_ops() {
    let s = start(Door::Http).await;
    let mut w = Wire::dial(&s.addr).await;
    w.send("GET /openapi.json HTTP/1.1\r\nHost: x\r\n\r\n")
        .await;
    let (status, h) = w.head().await;
    assert_eq!(status, 200);
    let n: usize = header(&h, "content-length").unwrap().parse().unwrap();
    let doc = String::from_utf8(w.exact(n).await).unwrap();
    for want in [
        r#""operationId": "post_echo""#,
        r#""operationId": "post_count""#,
        r#""operationId": "post_relay""#,
        r#""text/event-stream""#,
        r#""application/octet-stream""#,
    ] {
        assert!(doc.contains(want), "the document lacks {want}");
    }
}

/// The documents beside the source were projected from THIS declaration.
#[test]
fn the_documents_are_current() {
    let written =
        std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/gen/manifest.json")).unwrap();
    assert_eq!(
        written.trim_end(),
        stream::Chat::manifest(),
        "the declaration changed since `make gen`"
    );
}

/// A trailer reaches an HTTP/1.1 client that asked for trailers, after the
/// last chunk, and its name went out in the head.
#[tokio::test(flavor = "multi_thread")]
async fn a_trailer_follows_the_stream() {
    let s = start(Door::Http).await;
    let mut w = Wire::dial(&s.addr).await;
    w.post("/v1/count", r#"{"n":2,"everyMs":1}"#, "TE: trailers\r\n")
        .await;
    let (status, h) = w.head().await;
    assert_eq!(status, 200);
    assert_eq!(header(&h, "trailer"), Some("x-sent"));
    let got = events(&mut w, Instant::now()).await;
    assert_eq!(got.len(), 3);
    assert_eq!(w.trailers().await, vec!["x-sent: 2"]);
}

/// A raw op relays bytes in both directions at once: each chunk of the
/// request comes back before the next one is sent.
#[tokio::test(flavor = "multi_thread")]
async fn a_relay_answers_each_chunk_as_it_arrives() {
    let s = start(Door::Http).await;
    let mut w = Wire::dial(&s.addr).await;
    w.send("POST /v1/relay HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\n\r\n")
        .await;
    w.send("5\r\nhello\r\n").await;
    let (status, h) = w.head().await;
    assert_eq!(status, 200);
    assert_eq!(header(&h, "content-type"), Some("text/plain"));
    assert_eq!(w.chunk().await.unwrap(), b"hello");
    w.send("6\r\n world\r\n").await;
    assert_eq!(w.chunk().await.unwrap(), b" world");
    w.send("0\r\n\r\n").await;
    assert_eq!(w.chunk().await, None);
    assert!(w.trailers().await.is_empty());
}

/// HTTP/2 over cleartext on the same port: the stream arrives frame by frame
/// and its trailer after the last.
#[tokio::test(flavor = "multi_thread")]
async fn http2_streams_and_carries_the_trailer() {
    use http_body_util::BodyExt;
    let s = start(Door::Http).await;
    let tcp = TcpStream::connect(&s.addr).await.unwrap();
    let (mut send, conn) = hyper::client::conn::http2::handshake(
        hyper_util::rt::TokioExecutor::new(),
        hyper_util::rt::TokioIo::new(tcp),
    )
    .await
    .unwrap();
    tokio::spawn(conn);
    let req = hyper::Request::post(format!("http://{}/v1/count", s.addr))
        .header("content-type", "application/json")
        .body(http_body_util::Full::new(bytes::Bytes::from_static(
            br#"{"n":3,"everyMs":150}"#,
        )))
        .unwrap();
    let res = send.send_request(req).await.unwrap();
    assert_eq!(res.status(), 200);
    assert_eq!(res.version(), hyper::Version::HTTP_2);
    let mut body = res.into_body();
    let since = Instant::now();
    let mut data = Vec::new();
    let mut stamps = Vec::new();
    let mut trailers = None;
    while let Some(frame) = body.frame().await {
        let frame = frame.unwrap();
        if let Some(d) = frame.data_ref() {
            data.extend_from_slice(d);
            stamps.push(since.elapsed());
        } else if let Ok(t) = frame.into_trailers() {
            trailers = Some(t);
        }
    }
    assert_eq!(
        String::from_utf8(data).unwrap(),
        "data: {\"i\":1}\n\ndata: {\"i\":2}\n\ndata: {\"i\":3}\n\ndata: [DONE]\n\n"
    );
    assert!(
        stamps[1] - stamps[0] >= Duration::from_millis(75),
        "frames {stamps:?}"
    );
    let trailers = trailers.expect("no trailers");
    assert_eq!(trailers.get("x-sent").unwrap(), "3");
}

/// Shutdown stops new connections and lets the stream in flight finish.
#[tokio::test(flavor = "multi_thread")]
async fn shutdown_drains_what_is_in_flight() {
    let mut s = start(Door::Http).await;
    let mut w = Wire::dial(&s.addr).await;
    w.post("/v1/count", r#"{"n":4,"everyMs":150}"#, "").await;
    assert_eq!(w.head().await.0, 200);
    assert!(w.chunk().await.is_some());

    s.stop.take().unwrap().send(()).unwrap();
    let mut refused = false;
    for _ in 0..100 {
        if TcpStream::connect(&s.addr).await.is_err() {
            refused = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert!(refused, "a new connection was accepted after shutdown");

    let rest = events(&mut w, Instant::now()).await;
    assert_eq!(rest.last().unwrap().0, "data: [DONE]", "the stream was cut");
    tokio::time::timeout(Duration::from_secs(5), s.done)
        .await
        .expect("serve did not return once drained")
        .unwrap()
        .unwrap();
}

/// The ZAP door streams the same op: a head frame, a data frame as each event
/// is produced, an end frame holding the trailer.
#[tokio::test(flavor = "multi_thread")]
async fn the_zap_door_streams_frame_by_frame() {
    let s = start(Door::Zap).await;
    let mut conn = TcpStream::connect(&s.addr).await.unwrap();
    let headers = zip::zap::block([(&b"Content-Type"[..], &b"application/json"[..])]);
    let frame = zip::zap::request("POST", "/v1/count", &headers, br#"{"n":3,"everyMs":150}"#);
    conn.write_all(&(frame.len() as u32).to_be_bytes())
        .await
        .unwrap();
    conn.write_all(&frame).await.unwrap();

    let since = Instant::now();
    let head = zip::zaphttp::read_frame(&mut conn).await.unwrap().unwrap();
    assert_eq!(zip::zap::kind(&head), Some(zip::zap::FRAME_HEAD));
    let r = zip::zap::Response::wrap(&head).unwrap();
    assert_eq!(r.status(), 200);
    let names: Vec<_> = zip::zap::pairs(r.headers())
        .into_iter()
        .map(|(k, v)| {
            (
                String::from_utf8_lossy(k).into_owned(),
                String::from_utf8_lossy(v).into_owned(),
            )
        })
        .collect();
    assert!(
        names.contains(&("content-type".into(), "text/event-stream".into())),
        "{names:?}"
    );
    assert!(
        !names.iter().any(|(k, _)| k == "trailer"),
        "Trailer is the frame's slot, not a header"
    );

    let mut chunks = Vec::new();
    loop {
        let f = zip::zaphttp::read_frame(&mut conn).await.unwrap().unwrap();
        match zip::zap::kind(&f) {
            Some(zip::zap::FRAME_DATA) => {
                let c = zip::zap::chunk(&f).unwrap().to_vec();
                chunks.push((String::from_utf8(c).unwrap(), since.elapsed()));
            }
            Some(zip::zap::FRAME_END) => {
                let t = zip::zap::pairs(zip::zap::chunk(&f).unwrap());
                assert_eq!(t, vec![(&b"x-sent"[..], &b"3"[..])]);
                break;
            }
            other => panic!("frame type {other:?} inside a stream"),
        }
    }
    assert_eq!(chunks.len(), 4, "{chunks:?}");
    assert_eq!(chunks[0].0, "data: {\"i\":1}\n\n");
    assert_eq!(chunks[3].0, "data: [DONE]\n\n");
    assert!(
        chunks[1].1 - chunks[0].1 >= Duration::from_millis(75),
        "{chunks:?}"
    );

    // One exchange done, the connection answers the next.
    let frame = zip::zap::request("POST", "/v1/echo", &headers, br#"{"text":"zap"}"#);
    conn.write_all(&(frame.len() as u32).to_be_bytes())
        .await
        .unwrap();
    conn.write_all(&frame).await.unwrap();
    let f = zip::zaphttp::read_frame(&mut conn).await.unwrap().unwrap();
    let r = zip::zap::Response::wrap(&f).unwrap();
    assert_eq!((r.status(), r.body()), (200, &br#"{"text":"zap"}"#[..]));
}

/// A stream still going when the grace runs out is cut, and its producer
/// dropped: shutdown is bounded by the grace, not by the longest answer.
#[tokio::test(flavor = "multi_thread")]
async fn shutdown_ends_what_outlives_the_grace() {
    for door in [Door::Http, Door::Zap] {
        let mut s = start_with(door, Duration::from_millis(300)).await;
        let mut conn = TcpStream::connect(&s.addr).await.unwrap();
        let body = br#"{"n":100000,"everyMs":10}"#;
        match door {
            Door::Http => {
                let head = format!(
                    "POST /v1/count HTTP/1.1\r\nHost: x\r\nContent-Length: {}\r\n\r\n",
                    body.len()
                );
                conn.write_all(head.as_bytes()).await.unwrap();
                conn.write_all(body).await.unwrap();
            }
            Door::Zap => {
                let frame = zip::zap::request("POST", "/v1/count", &[], body);
                conn.write_all(&(frame.len() as u32).to_be_bytes())
                    .await
                    .unwrap();
                conn.write_all(&frame).await.unwrap();
            }
        }
        while s.open.load(Ordering::SeqCst) != 1 {
            tokio::time::sleep(Duration::from_millis(1)).await;
        }
        let stopped = Instant::now();
        s.stop.take().unwrap().send(()).unwrap();
        tokio::time::timeout(Duration::from_secs(5), s.done)
            .await
            .expect("serve outlived its grace")
            .unwrap()
            .unwrap();
        let took = stopped.elapsed();
        assert!(
            took >= Duration::from_millis(300),
            "serve returned in {took:?}, inside its grace"
        );
        while s.open.load(Ordering::SeqCst) != 0 {
            assert!(
                stopped.elapsed() < Duration::from_secs(5),
                "the producer outlived the grace"
            );
            tokio::time::sleep(Duration::from_millis(1)).await;
        }
        // The client reads to the end of what it was sent and finds the
        // connection closed, the answer cut.
        let mut rest = Vec::new();
        let _ = tokio::time::timeout(Duration::from_secs(5), conn.read_to_end(&mut rest)).await;
    }
}
