// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// What a declaration says about itself that its types do not.
//
// A Go declaration says it in a struct tag; a Rust one in an attribute; here it
// is an annotate attribute behind a macro. The compiler discards every one of
// them, so a wire name costs nothing at run time — and zipc, which reads this
// source with the compiler's own parser, sees them all.
//
// Standard [[attributes]] are not usable for this: clang hands them back with no
// recoverable text, so what a declaration said would be unreadable by the pass
// that has to read it.

#ifndef ZIP_MARK_HPP_
#define ZIP_MARK_HPP_

// Only clang carries an annotation into an AST anything can read, and clang is
// what reads this source. Every other compiler is told nothing, which is the
// truth: the note is for the pass, the program is unchanged either way.
#if defined(__clang__)
#define ZIP_NOTE(text) __attribute__((annotate(text)))
#else
#define ZIP_NOTE(text)
#endif

// On a member: what the wire calls it, where a URL carries it, which header it
// reads, and whether the handler refuses the request without it.
#define ZIP_JSON(name) ZIP_NOTE("json:" name)
#define ZIP_URL(name) ZIP_NOTE("url:" name)
#define ZIP_HEADER(name) ZIP_NOTE("header:" name)
#define ZIP_REQUIRED ZIP_NOTE("required")

// On a member whose type states its own wire form — a number carried as a
// quoted decimal, an id carried as text. The argument is the JSON Schema it
// states, which is the same vocabulary the document speaks.
#define ZIP_SCHEMA(json) ZIP_NOTE("schema:" json)
// On a member whose value reads and writes itself as ONE WORD, so a URL can
// carry it whatever it is made of.
#define ZIP_TEXT ZIP_NOTE("text")

// On a handler: what it is called on every surface, what it is grouped under,
// which statuses it may answer with, and which headers it may set.
#define ZIP_ID(name) ZIP_NOTE("id:" name)
#define ZIP_SUMMARY(line) ZIP_NOTE("summary:" line)
#define ZIP_TAGS(list) ZIP_NOTE("tags:" list)
#define ZIP_STATUS(list) ZIP_NOTE("status:" list)
#define ZIP_ANSWERS(list) ZIP_NOTE("header:" list)

#endif  // ZIP_MARK_HPP_
