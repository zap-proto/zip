// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command zipc writes a service's projections from its manifest.
//
// The manifest says what a service declares. It is written by whatever can read
// that language's own source — Go by reflecting on itself, C++ by a pass over
// its source with the compiler's parser, Rust by a macro inside the compiler —
// and everything downstream is written here, once, for all three.
//
//	zipc <manifest.json> -o gen/                 the document, the tools, the CLI, the schema
//	zipc <manifest.json> -o gen/ -lang cpp       and the C++ reader and writer for each type
//
// It runs at BUILD time and links into nothing: a service that has been through
// here carries no Go at run time, in any language.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/manifest"
)

func main() {
	out := flag.String("o", ".", "where to write the projections")
	lang := flag.String("lang", "", "also write this language's reader and writer: cpp")
	pkg := flag.String("pkg", "", "the ZAP schema's package name (default: the app's name)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: zipc <manifest.json> [-o dir] [-lang cpp] [-pkg name]")
		os.Exit(2)
	}

	body, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		die(err)
	}
	var m manifest.App
	if err := json.Unmarshal(body, &m); err != nil {
		die(fmt.Errorf("%s: %w", flag.Arg(0), err))
	}
	if len(m.Ops) == 0 {
		// Every projection of an empty registry is empty. Emitting one would
		// publish a file that looks like an API and describes nothing.
		die(fmt.Errorf("%s: the manifest declares no operations", flag.Arg(0)))
	}
	if *pkg == "" {
		*pkg = m.Name
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		die(err)
	}

	write(*out, "openapi.json", pretty(zip.Document(&m)))
	write(*out, "mcp.json", pretty(zip.Tools(&m)))
	write(*out, "cli.json", pretty(zip.Commands(&m)))
	write(*out, *pkg+".zap", []byte(zip.ZAP(*pkg, &m).String()))
	if *lang == "cpp" {
		write(*out, m.Name+".zip.hpp", zip.CppJSON(&m))
	}
}

// pretty is one artifact's bytes. Indented, because these are files a person
// reads and diffs; the served document is the same value compacted by the
// service itself.
func pretty(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		die(err)
	}
	return append(b, '\n')
}

func write(dir, name string, body []byte) {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		die(err)
	}
	fmt.Fprintf(os.Stderr, "zipc: %s (%d bytes)\n", path, len(body))
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "zipc:", err)
	os.Exit(1)
}
