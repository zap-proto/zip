// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! Every verb, and an address with a hole in it.
//!
//! The info example is a conformance corpus: the same service written three
//! times, compared byte for byte, and it asks only questions. So the rest of
//! what a service does — writing, addressing one thing by name, answering
//! nothing at all — is declared here, where changing it costs no other
//! language its comparison.

use zip::Wire;

/// Note is one note, by name.
#[derive(Wire, Default)]
pub struct Note {
    /// Name is what the note is filed under.
    #[zip(json = "name", required)]
    pub name: String,
    /// Body is the note itself.
    #[zip(json = "body")]
    pub body: String,
}

/// Named addresses one note.
#[derive(Wire, Default)]
pub struct Named {
    /// Name is the note to act on.
    #[zip(json = "name", required)]
    pub name: String,
}

/// Notes are the notes there are.
#[derive(Wire, Default)]
pub struct Notes {
    /// Names are the notes, by name.
    #[zip(json = "names")]
    pub names: Vec<String>,
}

/// Ledger keeps notes.
pub struct Ledger;

#[zip::ops(app = "notes", title = "Notes", version = "1.0.0")]
impl Ledger {
    /// Notes are every note there is.
    #[get("/notes")]
    fn list(&self) -> Result<Notes, zip::Error> {
        Ok(Notes {
            names: vec!["one".into()],
        })
    }

    /// Note is one note, addressed by name.
    #[get("/notes/:name")]
    fn read(&self, arg: &Named) -> Result<Note, zip::Error> {
        Ok(Note {
            name: arg.name.clone(),
            body: "read".into(),
        })
    }

    /// Write files a note under a name.
    #[post("/notes")]
    fn write(&self, arg: &Note) -> Result<Note, zip::Error> {
        Ok(Note {
            name: arg.name.clone(),
            body: arg.body.clone(),
        })
    }

    /// Replace overwrites the note a name is filed under.
    #[put("/notes/:name")]
    fn replace(&self, arg: &Note) -> Result<Note, zip::Error> {
        Ok(Note {
            name: arg.name.clone(),
            body: arg.body.clone(),
        })
    }

    /// Amend changes part of a note.
    #[patch("/notes/:name")]
    fn amend(&self, arg: &Note) -> Result<Note, zip::Error> {
        Ok(Note {
            name: arg.name.clone(),
            body: arg.body.clone(),
        })
    }

    /// Retire removes a note.
    #[delete("/notes/:name")]
    fn retire(&self, arg: &Named) -> Result<Named, zip::Error> {
        Ok(Named {
            name: arg.name.clone(),
        })
    }
}

fn app() -> zip::App {
    Ledger.ops()
}

fn ask(app: &zip::App, method: &str, target: &str, body: &str) -> (u16, String) {
    let a = app.answer(method, target, Vec::new(), body.as_bytes().to_vec());
    (a.status, String::from_utf8(a.body).unwrap())
}

#[test]
fn every_verb_is_registered_at_its_own_address() {
    let mut got = app().routes();
    got.sort();
    assert_eq!(
        got,
        vec![
            ("DELETE", "/notes/:name"),
            ("GET", "/notes"),
            ("GET", "/notes/:name"),
            ("PATCH", "/notes/:name"),
            ("POST", "/notes"),
            ("PUT", "/notes/:name"),
        ]
    );
}

#[test]
fn a_hole_in_the_address_binds_by_the_field_it_names() {
    let (status, body) = ask(&app(), "GET", "/notes/one", "");
    assert_eq!(status, 200);
    assert_eq!(body, r#"{"name":"one","body":"read"}"#);
}

#[test]
fn a_body_binds_on_the_verbs_that_carry_one() {
    // POST addresses the collection, so the body says which note this is; PUT
    // and PATCH address one note, so the address does and the body carries the
    // rest.
    let (status, body) = ask(&app(), "POST", "/notes", r#"{"name":"two","body":"b"}"#);
    assert_eq!(status, 200);
    assert_eq!(body, r#"{"name":"two","body":"b"}"#);
    for verb in ["PUT", "PATCH"] {
        let (status, body) = ask(&app(), verb, "/notes/two", r#"{"body":"b"}"#);
        assert_eq!(status, 200, "{verb}");
        assert_eq!(body, r#"{"name":"two","body":"b"}"#, "{verb}");
    }
}

#[test]
fn the_address_wins_over_the_body_it_disagrees_with() {
    // A hole in the address is where the thing IS. A body that names another
    // one is not a second opinion about which note this is.
    let (status, body) = ask(&app(), "PUT", "/notes/one", r#"{"name":"two","body":"b"}"#);
    assert_eq!(status, 200);
    assert_eq!(body, r#"{"name":"one","body":"b"}"#);
}

#[test]
fn the_same_address_under_a_verb_it_does_not_serve_is_refused() {
    let (status, _) = ask(&app(), "DELETE", "/notes", "");
    assert_eq!(status, 405);
}

#[test]
fn a_required_field_the_request_never_gave_is_refused() {
    let (status, body) = ask(&app(), "POST", "/notes", r#"{"body":"b"}"#);
    assert_eq!(status, 400);
    assert!(body.contains("name"), "{body}");
}

/// pin holds the manifest this service compiles to, where the projector's own
/// tests can read it. Set ZIP_GOLDEN to rewrite it after a deliberate change.
///
/// It is checked in rather than projected here because projecting is Go's job
/// and this crate links no Go. What the two sides share is the file: this test
/// says the front end still writes it, and notes_test.go says the projections
/// still read it.
fn pin_manifest(m: &str) {
    let at =
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/notes/manifest.json");
    if std::env::var_os("ZIP_GOLDEN").is_some() {
        std::fs::create_dir_all(at.parent().unwrap()).unwrap();
        std::fs::write(&at, format!("{m}\n")).unwrap();
        return;
    }
    let want = std::fs::read_to_string(&at).unwrap_or_else(|e| panic!("{}: {e}", at.display()));
    assert_eq!(m, want.trim_end(), "the manifest moved");
}

#[test]
fn the_manifest_says_what_was_declared() {
    let m = Ledger::manifest();
    // The verbs and the addresses, as the projector will read them.
    for want in [
        r#""method":"GET","path":"/notes""#,
        r#""method":"GET","path":"/notes/:name""#,
        r#""method":"POST","path":"/notes""#,
        r#""method":"PUT","path":"/notes/:name""#,
        r#""method":"PATCH","path":"/notes/:name""#,
        r#""method":"DELETE","path":"/notes/:name""#,
    ] {
        assert!(m.contains(want), "manifest does not carry {want}:\n{m}");
    }
    // The prose is the doc comment, at the scope it was written at: the
    // handler's above the op, the field's above the field.
    assert!(m.contains("Write files a note under a name."), "{m}");
    assert!(m.contains("Name is what the note is filed under."), "{m}");
    pin_manifest(&m);
}
