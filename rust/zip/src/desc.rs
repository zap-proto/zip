// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! What a type says about itself, in the running service.
//!
//! The same description the manifest carries, minus everything only a projector
//! needs. A binder asks two questions of a field — what is it called on the
//! wire, and what can a URL put in it — and this answers exactly those, so
//! there is no second table of names for a request to be bound against.
//!
//! It is a const. [`Wire::describe`] hands back a `&'static`, so a type's
//! description costs nothing at run time and cannot be different on the second
//! call than on the first.

/// TypeDesc is one described type.
pub struct TypeDesc {
    /// id is the name every projection refers to this type by.
    pub id: &'static str,
    /// text says the value is carried as one word, so a URL can hold it.
    pub text: bool,
    pub fields: &'static [FieldDesc],
}

/// FieldDesc is one field of a described type.
pub struct FieldDesc {
    /// name is the field as the source declares it; json is what the wire calls
    /// it, "-" for a field the body does not carry.
    pub name: &'static str,
    pub json: &'static str,
    /// url is the name a URL carries it under, "" when that is the wire name
    /// and "-" when the URL does not carry it at all.
    pub url: &'static str,
    /// header is the request header it reads.
    pub header: &'static str,
    pub required: bool,
    /// scalar is what a URL can put here, which is what turns one query value
    /// into the JSON the one decoder reads.
    pub scalar: Scalar,
}

impl FieldDesc {
    /// url_name is the name a URL carries this field under.
    pub fn url_name(&self) -> &'static str {
        if self.url.is_empty() {
            self.json
        } else {
            self.url
        }
    }
}

/// Scalar is what a URL can carry into a field.
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Scalar {
    Text,
    Number,
    Bool,
    /// List is a repeated value, spelled comma-separated in one URL value.
    List(&'static Scalar),
    /// Named is a type that describes itself; a URL carries it when it is
    /// carried as text, and otherwise it rides the body.
    Named,
    /// Body is a value a URL has no spelling for.
    Body,
}

/// Wire is a type that says what it is and carries itself over JSON.
///
/// Two halves of one fact. `describe` is what every projection reads — the
/// field names, the URL names, the prose — and `write_json`/`read_json` are the
/// bytes those names describe. They are derived together from one declaration,
/// which is what stops a document describing a field the decoder reads under
/// another name.
pub trait Wire: Sized {
    fn describe() -> &'static TypeDesc;

    /// stated is this type's whole description — its fields, their wire names,
    /// their prose — as one manifest entry.
    ///
    /// It is a `&'static str` built by the macro from the declaration, so what
    /// the service publishes is what the compiler read, and there is no file
    /// beside the source to fall out of step with it.
    fn stated() -> &'static str;

    /// reach adds this type and everything it holds to a manifest, once each.
    /// A macro sees only the item it is on, so the graph is walked by the types
    /// themselves: each one claims its entry, then asks the types of its fields.
    fn reach(into: &mut Vec<(&'static str, &'static str)>);

    fn write_json(&self, out: &mut String);
    fn read_json(v: &crate::Json) -> Result<Self, crate::Error>;
}
