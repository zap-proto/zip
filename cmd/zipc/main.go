// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// zipc projects an app, from a description of it rather than from a program.
//
// It reads a [zip.Manifest] — one JSON document, or a directory of the
// fragments a front end writes as it compiles — and writes the OpenAPI
// document, the MCP tool list, the CLI command tree and the ZAP IDL. Nothing it
// reads is Go and nothing it writes is Go, so the language that DECLARED the
// ops is the language it was declared in and no other:
//
//	Go     App.Manifest(), by reflection, at run time
//	Rust   #[zip::ops], by proc-macro, as the crate compiles
//	C++    a libclang walk, as the build runs
//
// It is a build-time tool. A service built with it links none of it: the
// documents are files, the ZAP accessors come from zapgen, and what runs is the
// service's own language all the way down.
//
// Usage:
//
//	zipc [-out DIR] [-package NAME] MANIFEST
//
// MANIFEST is the document a front end wrote: `App.Manifest()` in Go, what
// `Service::manifest()` prints in Rust, what the libclang pass emits in C++.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/zap-proto/zip"
)

func main() {
	out := flag.String("out", ".", "directory to write the projections into")
	pkg := flag.String("package", "", "package name for the ZAP schema (default: the app's name)")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *out, *pkg); err != nil {
		fmt.Fprintln(os.Stderr, "zipc:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: zipc [-out DIR] [-package NAME] MANIFEST")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "MANIFEST is the description a front end wrote of one app.")
}

func run(in, out, pkg string) error {
	m, err := read(in)
	if err != nil {
		return err
	}
	if err := m.Check(); err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	m.Settle()
	if pkg == "" {
		pkg = m.App
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{}
	if files["openapi.json"], err = pretty(zip.ProjectOpenAPI(m)); err != nil {
		return err
	}
	if files["mcp.json"], err = pretty(zip.ProjectMCP(m)); err != nil {
		return err
	}
	if files["cli.json"], err = pretty(zip.ProjectCLI(m)); err != nil {
		return err
	}
	files[pkg+".zap"] = []byte(zip.ProjectZAP(pkg, m).String())

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		at := filepath.Join(out, name)
		if err := os.WriteFile(at, files[name], 0o644); err != nil {
			return err
		}
		fmt.Println("zipc:", at)
	}
	return nil
}

// read is one manifest.
func read(at string) (zip.Manifest, error) {
	var m zip.Manifest
	if err := load(at, &m); err != nil {
		return m, err
	}
	return m, nil
}

func load(at string, into any) error {
	b, err := os.ReadFile(at)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, into); err != nil {
		return fmt.Errorf("%s: %w", at, err)
	}
	return nil
}

func pretty(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
