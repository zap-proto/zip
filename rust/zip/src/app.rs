// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The app: a typed-op registry, and the two doors onto it.
//!
//! An op is registered once. What answers a REST request and what answers a ZAP
//! frame are the same closure, reached the same way — decode, run, write — so a
//! browser and a sibling service are talking to one handler and not to two
//! spellings of one.

use std::collections::HashMap;

use crate::{Error, FieldDesc, Json, Scalar, TypeDesc, Wire};

/// Input is one request, before it is a value: what the URL carried, what the
/// headers said, and the bytes of the body.
pub struct Input {
    pub method: String,
    pub path: String,
    pub query: Vec<(String, String)>,
    pub params: Vec<(String, String)>,
    pub headers: Vec<(String, String)>,
    pub body: Vec<u8>,
}

impl Input {
    /// header reads one request header, case-insensitively as HTTP names are.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(k, _)| k.eq_ignore_ascii_case(name))
            .map(|(_, v)| v.as_str())
    }
}

/// Call is what an op does: a request in, the answer's JSON out. None is an op
/// that answers nothing, which is a 204.
pub type Call = Box<dyn Fn(&Input) -> Result<Option<String>, Error> + Send + Sync>;

/// Op is one registered operation.
pub struct Op {
    pub method: &'static str,
    pub path: &'static str,
    /// input is the description of what this op takes, or None for an op that
    /// takes nothing.
    pub input: Option<&'static TypeDesc>,
    pub call: Call,
    segments: Vec<Segment>,
}

enum Segment {
    Literal(String),
    Param(String),
    Rest(String),
}

/// App is a service: its ops, and what it says about itself.
pub struct App {
    pub name: &'static str,
    pub title: &'static str,
    pub version: &'static str,
    pub description: &'static str,
    ops: Vec<Op>,
    files: HashMap<String, (&'static str, &'static str)>,
}

impl App {
    pub fn new(
        name: &'static str,
        title: &'static str,
        version: &'static str,
        description: &'static str,
    ) -> Self {
        App {
            name,
            title,
            version,
            description,
            ops: Vec::new(),
            files: HashMap::new(),
        }
    }

    /// op registers one operation. The macro calls this; a service does not.
    pub fn op(
        &mut self,
        method: &'static str,
        path: &'static str,
        input: Option<&'static TypeDesc>,
        call: Call,
    ) -> &mut Self {
        self.ops.push(Op {
            method,
            path,
            input,
            call,
            segments: split(path),
        });
        self
    }

    /// serve publishes a file at an address: the projections zipc wrote, so a
    /// caller can read the document from the service that implements it.
    pub fn serve(&mut self, path: &str, kind: &'static str, body: &'static str) -> &mut Self {
        self.files.insert(path.to_string(), (kind, body));
        self
    }

    /// SPEC is where a zip service publishes its OpenAPI document.
    ///
    /// It is a convention and not a preference: a client that has an address
    /// and nothing else asks here, and gets back the registry in wire form —
    /// which is enough to build a command line, a tool list or a typed client
    /// for a service it does not link. zip's Go client asks exactly this
    /// address, so a Rust service that published its document somewhere else
    /// was a service no zip client could discover.
    pub const SPEC: &'static str = "/.well-known/openapi.json";

    /// TOOLS is where it publishes the MCP tool list.
    pub const TOOLS: &'static str = "/.well-known/mcp.json";

    /// documents publishes what zipc projected, at the addresses zip names for
    /// them. An empty one is not published: a service that has not been
    /// projected yet says so by 404 rather than by serving an empty document.
    pub fn documents(&mut self, openapi: &'static str, tools: &'static str) -> &mut Self {
        if !openapi.is_empty() {
            self.serve(Self::SPEC, "application/json", openapi);
        }
        if !tools.is_empty() {
            self.serve(Self::TOOLS, "application/json", tools);
        }
        self
    }

    pub fn ops_len(&self) -> usize {
        self.ops.len()
    }

    /// routes is every registered address, for a startup line.
    pub fn routes(&self) -> Vec<(&'static str, &'static str)> {
        self.ops.iter().map(|o| (o.method, o.path)).collect()
    }

    /// answer runs a request: find the op, decode, run, write. It is the ONE
    /// path in, so a REST request and a ZAP frame cannot diverge.
    pub fn answer(
        &self,
        method: &str,
        target: &str,
        headers: Vec<(String, String)>,
        body: Vec<u8>,
    ) -> Answer {
        let (path, query) = split_target(target);
        if method == "GET" {
            if let Some((kind, text)) = self.files.get(path) {
                return Answer {
                    status: 200,
                    kind,
                    body: text.as_bytes().to_vec(),
                };
            }
        }
        let mut allowed = false;
        for op in &self.ops {
            let Some(params) = op.bind_path(path) else {
                continue;
            };
            if op.method != method {
                allowed = true;
                continue;
            }
            let input = Input {
                method: method.to_string(),
                path: path.to_string(),
                query,
                params,
                headers,
                body,
            };
            return match (op.call)(&input) {
                Ok(Some(json)) => Answer {
                    status: 200,
                    kind: "application/json",
                    body: json.into_bytes(),
                },
                Ok(None) => Answer {
                    status: 204,
                    kind: "application/json",
                    body: Vec::new(),
                },
                Err(e) => Answer {
                    status: e.status,
                    kind: "application/problem+json",
                    body: e.problem().into_bytes(),
                },
            };
        }
        let e = if allowed {
            Error::new(405, "that address does not answer this method")
        } else {
            Error::missing("no such address")
        };
        Answer {
            status: e.status,
            kind: "application/problem+json",
            body: e.problem().into_bytes(),
        }
    }
}

/// Answer is what a door writes back.
pub struct Answer {
    pub status: u16,
    pub kind: &'static str,
    pub body: Vec<u8>,
}

impl Op {
    /// bind_path matches this op's pattern against an address, answering the
    /// segments it captured.
    fn bind_path(&self, path: &str) -> Option<Vec<(String, String)>> {
        let mut got = Vec::new();
        let mut parts = path.split('/').filter(|s| !s.is_empty());
        let mut want = self.segments.iter();
        loop {
            match want.next() {
                None => {
                    return if parts.next().is_none() {
                        Some(got)
                    } else {
                        None
                    }
                }
                Some(Segment::Rest(name)) => {
                    let rest: Vec<&str> = parts.collect();
                    got.push((name.clone(), rest.join("/")));
                    return Some(got);
                }
                Some(Segment::Literal(lit)) => {
                    if parts.next()? != lit {
                        return None;
                    }
                }
                Some(Segment::Param(name)) => {
                    got.push((name.clone(), parts.next()?.to_string()));
                }
            }
        }
    }
}

fn split(path: &str) -> Vec<Segment> {
    path.split('/')
        .filter(|s| !s.is_empty())
        .map(|s| {
            if let Some(name) = s.strip_prefix(':') {
                Segment::Param(name.to_string())
            } else if s == "*" || s == "+" {
                Segment::Rest("*1".to_string())
            } else {
                Segment::Literal(s.to_string())
            }
        })
        .collect()
}

fn split_target(target: &str) -> (&str, Vec<(String, String)>) {
    let (path, rest) = match target.split_once('?') {
        Some((p, q)) => (p, q),
        None => (target, ""),
    };
    let mut query = Vec::new();
    for pair in rest.split('&').filter(|s| !s.is_empty()) {
        let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
        query.push((unescape(k), unescape(v)));
    }
    (path, query)
}

/// unescape reads percent-encoding out of one URL value.
fn unescape(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'+' => {
                out.push(b' ');
                i += 1;
            }
            b'%' if i + 2 < b.len() => {
                match u8::from_str_radix(std::str::from_utf8(&b[i + 1..i + 3]).unwrap_or("zz"), 16)
                {
                    Ok(v) => {
                        out.push(v);
                        i += 3;
                    }
                    Err(_) => {
                        out.push(b'%');
                        i += 1;
                    }
                }
            }
            c => {
                out.push(c);
                i += 1;
            }
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// bind is one request as the value the handler takes.
///
/// The body carries what a body can, and the URL carries the rest — and where
/// both spoke, the URL wins, because a URL is what addresses the resource. That
/// is one rule for every op: a GET's whole input arrives in the URL and a POST's
/// path parameters still bind, without either being a special case.
pub fn bind<T: Wire>(input: &Input) -> Result<T, Error> {
    let desc = T::describe();
    let mut object = match body_of(input)? {
        Json::Map(m) => m,
        Json::Null => HashMap::new(),
        other => return T::read_json(&other), // the body IS the whole value
    };
    for f in desc.fields {
        if !f.header.is_empty() {
            if let Some(v) = input.header(f.header) {
                object.insert(f.json.to_string(), scalar(v, &f.scalar));
            }
        }
    }
    for (name, value) in input.query.iter().chain(input.params.iter()) {
        if let Some(f) = field_for(desc, name) {
            object.insert(f.json.to_string(), scalar(value, &f.scalar));
        }
    }
    require(desc, &object)?;
    T::read_json(&Json::Map(object))
}

/// require refuses a request missing a value the op declared it cannot run
/// without.
///
/// It runs HERE, on the bound input, and not in the handler: the document says
/// the field is required, so a service that only checked it in some handlers
/// would publish a contract it keeps by habit. A field is missing when it is
/// absent or when it is the zero its type reads as — the same rule the Go side
/// applies, so one client sees one answer from either.
fn require(desc: &'static TypeDesc, object: &HashMap<String, Json>) -> Result<(), Error> {
    for f in desc.fields {
        if !f.required {
            continue;
        }
        let given = match object.get(f.json) {
            Some(Json::Text(s)) => !s.is_empty(),
            Some(Json::Number(n)) => *n != 0.0,
            Some(Json::Bool(b)) => *b,
            Some(Json::List(l)) => !l.is_empty(),
            Some(Json::Map(m)) => !m.is_empty(),
            Some(Json::Null) | None => false,
        };
        if !given {
            return Err(Error::bad(format!("field {:?} is required", f.json)));
        }
    }
    Ok(())
}

fn body_of(input: &Input) -> Result<Json, Error> {
    if input.body.is_empty() {
        return Ok(Json::Null);
    }
    let text = std::str::from_utf8(&input.body).map_err(|_| Error::bad("the body is not utf-8"))?;
    if text.trim().is_empty() {
        return Ok(Json::Null);
    }
    Json::parse(text)
}

/// field_for is the field a URL name binds to. "-" opts out, exactly as it does
/// for the body: a field can be body-only and a field can be URL-only, and the
/// two halves are asked separately because a route may mean two things by one
/// word.
fn field_for(desc: &'static TypeDesc, name: &str) -> Option<&'static FieldDesc> {
    desc.fields.iter().find(|f| {
        let url = f.url_name();
        url != "-" && url.eq_ignore_ascii_case(name)
    })
}

/// scalar is one URL value as the JSON its field expects. A list is spelled
/// comma-separated in one value, which is what `style: form, explode: false`
/// already publishes for a repeated parameter.
fn scalar(text: &str, want: &Scalar) -> Json {
    match want {
        Scalar::Bool => Json::Bool(text == "true" || text == "1"),
        Scalar::Number => text.parse().map(Json::Number).unwrap_or(Json::Null),
        Scalar::List(elem) => Json::List(
            text.split(',')
                .filter(|s| !s.is_empty())
                .map(|s| scalar(s, elem))
                .collect(),
        ),
        _ => Json::Text(text.to_string()),
    }
}
