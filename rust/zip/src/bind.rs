// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! One request, as the value the handler takes.
//!
//! The body carries what a body can, and the URL carries the rest — and where
//! both spoke, the URL wins, because a URL is what addresses the resource. That
//! is one rule for every op: a GET's whole input arrives in the URL and a POST's
//! path parameters still bind, without either being a special case.

use serde::de::DeserializeOwned;
use serde_json::{Map, Value};

use crate::{Body, Cx, Error, FieldDesc, Scalar, TypeDesc, Wire};

/// MAX_BODY is the largest body an op's input is read from, the ZAP door's
/// frame bound. An op that must take more takes a `Body` and reads it as it
/// arrives.
pub const MAX_BODY: usize = 32 << 20;

/// bind reads the body and binds the request to the op's input.
pub async fn bind<T: Wire + DeserializeOwned>(cx: &Cx, body: Body) -> Result<T, Error> {
    // A length past the bound is refused on its say-so, before a byte of it is
    // waited for; a body that does not declare one is counted as it arrives.
    if let Some(n) = cx
        .header("content-length")
        .and_then(|v| v.parse::<usize>().ok())
    {
        if n > MAX_BODY {
            return Err(Error::new(
                413,
                format!("the body is over {MAX_BODY} bytes"),
            ));
        }
    }
    let bytes = body.bytes(MAX_BODY).await?;
    value::<T>(cx, &bytes)
}

/// value is [bind] over a body already read.
pub fn value<T: Wire + DeserializeOwned>(cx: &Cx, body: &[u8]) -> Result<T, Error> {
    let desc = T::describe();
    let first = body.iter().find(|b| !b.is_ascii_whitespace());
    // What the URL and the headers add to the body. When they add nothing and
    // nothing is required, the body is the value and is read once, straight
    // into it: a chat request with an image in it is not copied through a tree
    // first.
    let mut from = Vec::new();
    for f in desc.fields {
        if !f.header.is_empty() {
            if let Some(v) = cx.header(f.header) {
                from.push((f, v));
            }
        }
    }
    for (name, v) in cx.query.iter().chain(cx.params.iter()) {
        if let Some(f) = field_for(desc, name) {
            from.push((f, v.as_str()));
        }
    }
    let required = desc.fields.iter().any(|f| f.required);
    if from.is_empty() && !required && first == Some(&b'{') {
        return serde_json::from_slice(body).map_err(refuse);
    }
    let mut object = if first.is_none() {
        Map::new()
    } else {
        match serde_json::from_slice::<Value>(body).map_err(refuse)? {
            Value::Object(m) => m,
            Value::Null => Map::new(),
            other => return serde_json::from_value(other).map_err(refuse), // the body IS the whole value
        }
    };
    for (f, v) in from {
        object.insert(f.json.to_string(), scalar(v, &f.scalar));
    }
    require(desc, &object)?;
    serde_json::from_value(Value::Object(object)).map_err(refuse)
}

fn refuse(e: serde_json::Error) -> Error {
    Error::bad(format!("the input does not read: {e}"))
}

/// require refuses a request missing a value the op declared it cannot run
/// without.
///
/// It runs HERE, on the bound input, and not in the handler: the document says
/// the field is required, so a service that only checked it in some handlers
/// would publish a contract it keeps by habit. A field is missing when it is
/// absent or when it is the zero its type reads as — the same rule the Go side
/// applies, so one client sees one answer from either.
fn require(desc: &'static TypeDesc, object: &Map<String, Value>) -> Result<(), Error> {
    for f in desc.fields {
        if !f.required {
            continue;
        }
        let given = match object.get(f.json) {
            Some(Value::String(s)) => !s.is_empty(),
            Some(Value::Number(n)) => n.as_f64() != Some(0.0),
            Some(Value::Bool(b)) => *b,
            Some(Value::Array(l)) => !l.is_empty(),
            Some(Value::Object(m)) => !m.is_empty(),
            Some(Value::Null) | None => false,
        };
        if !given {
            return Err(Error::bad(format!("field {:?} is required", f.json)));
        }
    }
    Ok(())
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
/// already publishes for a repeated parameter. A number is read as an integer
/// when it is one, since serde will not read 1.0 into a u32.
fn scalar(text: &str, want: &Scalar) -> Value {
    match want {
        Scalar::Bool => Value::Bool(text == "true" || text == "1"),
        Scalar::Number => {
            if let Ok(i) = text.parse::<i64>() {
                Value::from(i)
            } else if let Ok(u) = text.parse::<u64>() {
                Value::from(u)
            } else {
                text.parse::<f64>().map(Value::from).unwrap_or(Value::Null)
            }
        }
        Scalar::List(elem) => Value::Array(
            text.split(',')
                .filter(|s| !s.is_empty())
                .map(|s| scalar(s, elem))
                .collect(),
        ),
        _ => Value::String(text.to_string()),
    }
}
