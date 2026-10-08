// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! What the derive describes, what the binder reads, and what the door does
//! with a handler that breaks.

use std::sync::Arc;
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use zip::{Cx, Error, Wire};

/// Ask is a request whose fields arrive from everywhere a request has.
#[derive(Wire, Serialize, Deserialize, Default, Debug, PartialEq)]
#[serde(default, rename_all = "camelCase")]
struct Ask {
    /// Org is read from the gateway's header.
    #[zip(header = "X-Org-Id")]
    org_id: String,
    /// Model is required.
    #[zip(required)]
    model: String,
    /// MaxTokens rides the query on a GET.
    max_tokens: u32,
    /// Stop is a list.
    stop: Vec<String>,
    /// Extra is passed through unread.
    extra: Option<serde_json::Value>,
    /// Blob is bytes, as base64.
    #[serde(with = "zip::base64")]
    blob: Vec<u8>,
    /// Kept is never on the wire.
    #[serde(skip)]
    kept: u32,
    /// Named is renamed outright.
    #[serde(rename = "n")]
    named: Count,
}

/// Count is a 64-bit count, carried as a quoted decimal.
#[derive(Wire, Default, Debug, PartialEq, Clone, Copy)]
#[zip(text)]
struct Count(u64);

struct Svc;

#[zip::ops(app = "t", title = "T", version = "1.0.0")]
impl Svc {
    /// Ask answers the input it bound.
    #[get("/v1/ask/:model")]
    async fn ask(&self, arg: &Ask) -> Result<Ask, Error> {
        Ok(Ask {
            org_id: arg.org_id.clone(),
            model: arg.model.clone(),
            max_tokens: arg.max_tokens,
            stop: arg.stop.clone(),
            extra: arg.extra.clone(),
            blob: arg.blob.clone(),
            kept: 0,
            named: arg.named,
        })
    }

    /// Post answers its input, by value.
    #[post("/v1/post")]
    async fn post(&self, cx: &Cx, arg: Ask) -> Result<Ask, Error> {
        cx.set_status(201)?;
        cx.set_header("x-model", &arg.model)?;
        Ok(arg)
    }

    /// Fail refuses, with a header on the refusal.
    #[post("/v1/fail")]
    async fn fail(&self, cx: &Cx) -> Result<(), Error> {
        cx.set_header("retry-after", "7")?;
        Err(Error::new(429, "slow down"))
    }

    /// Break panics.
    #[post("/v1/break")]
    async fn broken(&self) -> Result<(), Error> {
        panic!("a handler bug");
    }
}

fn field(name: &str) -> &'static zip::FieldDesc {
    Ask::describe()
        .fields
        .iter()
        .find(|f| f.name == name)
        .unwrap()
}

#[test]
fn wire_names_are_serdes() {
    assert_eq!(field("org_id").json, "orgId");
    assert_eq!(field("org_id").header, "X-Org-Id");
    assert_eq!(field("max_tokens").json, "maxTokens");
    assert_eq!(field("kept").json, "-");
    assert_eq!(field("named").json, "n");
    assert!(field("model").required);
    let m = Svc::manifest();
    assert!(m.contains(r#""json":"maxTokens""#), "{m}");
    assert!(m.contains(r#""name":"extra","json":"extra","doc":"Extra is passed through unread.","type":{"prim":"any","opt":true}"#), "{m}");
    assert!(m.contains(r#""type":{"prim":"bytes"}"#), "{m}");
    assert!(m.contains(r#"{"id":"Count","name":"Count","kind":"scalar","spell":"Count","repr":"u64","text":true,"json":{"type":"string"}}"#), "{m}");
}

#[test]
fn a_text_type_is_one_word() {
    assert_eq!(
        serde_json::to_string(&Count(18446744073709551615)).unwrap(),
        "\"18446744073709551615\""
    );
    assert_eq!(serde_json::from_str::<Count>("\"42\"").unwrap(), Count(42));
    let e = serde_json::from_str::<Count>("\"x\"")
        .unwrap_err()
        .to_string();
    assert!(e.contains("not a Count"), "{e}");
}

async fn serve() -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap().to_string();
    let app = Arc::new(Svc.ops());
    tokio::spawn(zip::http::serve(
        app,
        listener,
        std::future::pending(),
        Duration::from_secs(1),
    ));
    addr
}

/// ask sends one request on its own connection and answers the status, the
/// head and the body.
async fn ask(addr: &str, req: &str) -> (u16, String, String) {
    let mut c = tokio::net::TcpStream::connect(addr).await.unwrap();
    c.write_all(req.as_bytes()).await.unwrap();
    let mut all = Vec::new();
    c.read_to_end(&mut all).await.unwrap();
    let all = String::from_utf8(all).unwrap();
    let (head, body) = all.split_once("\r\n\r\n").unwrap();
    let status = head.split_whitespace().nth(1).unwrap().parse().unwrap();
    (status, head.to_ascii_lowercase(), body.to_string())
}

#[tokio::test(flavor = "multi_thread")]
async fn a_request_binds_from_the_url_and_headers() {
    let addr = serve().await;
    let (status, _, body) = ask(
        &addr,
        "GET /v1/ask/kai?maxTokens=64&stop=a,b HTTP/1.1\r\nHost: x\r\nX-Org-Id: acme\r\nConnection: close\r\n\r\n",
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let got: serde_json::Value = serde_json::from_str(&body).unwrap();
    assert_eq!(
        got,
        serde_json::json!({"orgId": "acme", "model": "kai", "maxTokens": 64, "stop": ["a", "b"], "extra": null, "blob": "", "n": "0"})
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn a_body_binds_whole_and_the_answer_carries_what_cx_set() {
    let addr = serve().await;
    let body = r#"{"model":"kai","extra":{"tools":[1,2]},"blob":"emlw","n":"7","kept":9}"#;
    let req = format!(
        "POST /v1/post HTTP/1.1\r\nHost: x\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    let (status, head, out) = ask(&addr, &req).await;
    assert_eq!(status, 201, "{out}");
    assert!(head.contains("x-model: kai"), "{head}");
    let got: serde_json::Value = serde_json::from_str(&out).unwrap();
    assert_eq!(got["extra"], serde_json::json!({"tools": [1, 2]}));
    assert_eq!(got["blob"], "emlw");
    assert_eq!(got["n"], "7");
    assert!(got.get("kept").is_none(), "a skipped field rode the wire");

    // Not JSON is a 400, said as a problem document.
    let req =
        "POST /v1/post HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\nConnection: close\r\n\r\n{x}";
    let (status, head, _) = ask(&addr, req).await;
    assert_eq!(status, 400);
    assert!(
        head.contains("content-type: application/problem+json"),
        "{head}"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn a_refusal_keeps_its_status_and_the_headers_set() {
    let addr = serve().await;
    let (status, head, body) = ask(
        &addr,
        "POST /v1/fail HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
    )
    .await;
    assert_eq!(status, 429);
    assert!(head.contains("retry-after: 7"), "{head}");
    assert_eq!(
        body,
        r#"{"detail":"slow down","status":429,"title":"Too Many Requests","type":"about:blank"}"#
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn a_handler_that_panics_is_a_500_and_the_server_lives() {
    let addr = serve().await;
    let (status, _, body) = ask(
        &addr,
        "POST /v1/break HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
    )
    .await;
    assert_eq!(status, 500);
    assert!(body.contains("the handler panicked"), "{body}");
    let (status, _, _) = ask(
        &addr,
        "GET /v1/ask/kai HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
    )
    .await;
    assert_eq!(status, 200);
}

#[tokio::test(flavor = "multi_thread")]
async fn a_body_past_the_bound_is_refused_before_it_is_read() {
    let addr = serve().await;
    let n = zip::MAX_BODY + 1;
    let (status, _, _) = ask(
        &addr,
        &format!(
            "POST /v1/post HTTP/1.1\r\nHost: x\r\nContent-Length: {n}\r\nConnection: close\r\n\r\n"
        ),
    )
    .await;
    // Only the head was sent. A server that read before it counted would wait
    // here for 32 MiB that are never coming.
    assert_eq!(status, 413);
}
