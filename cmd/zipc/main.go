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
// MANIFEST is a .json file, or a directory holding ops/*.json and types/*.json
// fragments.
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
	fmt.Fprintln(os.Stderr, "MANIFEST is a manifest .json file, or a directory of ops/*.json")
	fmt.Fprintln(os.Stderr, "and types/*.json fragments written by a front end.")
}

func run(in, out, pkg string) error {
	m, err := read(in)
	if err != nil {
		return err
	}
	m.Settle()
	if m.Manifest != zip.ManifestVersion {
		return fmt.Errorf("%s: manifest version %d, this zipc reads %d", in, m.Manifest, zip.ManifestVersion)
	}
	if len(m.Ops) == 0 {
		// An empty registry projects an empty document, which is worse than no
		// document: it publishes that the service has nothing to offer.
		return fmt.Errorf("%s: no ops; there is nothing to project", in)
	}
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

// read is one manifest, from a file or from the fragments a compiler wrote.
func read(at string) (zip.Manifest, error) {
	info, err := os.Stat(at)
	if err != nil {
		return zip.Manifest{}, err
	}
	if !info.IsDir() {
		return one(at)
	}
	return assemble(at)
}

func one(at string) (zip.Manifest, error) {
	b, err := os.ReadFile(at)
	if err != nil {
		return zip.Manifest{}, err
	}
	var m zip.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return zip.Manifest{}, fmt.Errorf("%s: %w", at, err)
	}
	return m, nil
}

// assemble is one manifest out of the fragments a front end wrote as it
// compiled: one per impl block, one per described type.
//
// It is a merge and not a build. Nothing here decides anything about an op or a
// type — a fragment is already the finished description — so the order the
// files arrive in changes nothing, which is what lets a compiler write them as
// it reaches them.
func assemble(dir string) (zip.Manifest, error) {
	m := zip.Manifest{Manifest: zip.ManifestVersion}
	seen := map[string]bool{}

	ops, err := filepath.Glob(filepath.Join(dir, "ops", "*.json"))
	if err != nil {
		return m, err
	}
	sort.Strings(ops)
	for _, at := range ops {
		var part zip.Manifest
		if err := load(at, &part); err != nil {
			return m, err
		}
		if part.App != "" {
			m.App = part.App
		}
		if part.Title != "" {
			m.Title = part.Title
		}
		if part.Version != "" {
			m.Version = part.Version
		}
		if part.Description != "" {
			m.Description = part.Description
		}
		m.Ops = append(m.Ops, part.Ops...)
	}

	types, err := filepath.Glob(filepath.Join(dir, "types", "*.json"))
	if err != nil {
		return m, err
	}
	sort.Strings(types)
	for _, at := range types {
		var td zip.TypeDesc
		if err := load(at, &td); err != nil {
			return m, err
		}
		if seen[td.ID] {
			return m, fmt.Errorf("%s: two types described under %q; a name is how a field refers to a type, so it can only mean one", at, td.ID)
		}
		seen[td.ID] = true
		m.Types = append(m.Types, td)
	}
	sort.Slice(m.Types, func(i, j int) bool { return m.Types[i].ID < m.Types[j].ID })

	// A field naming a type nobody described is a dangling reference, and it is
	// found HERE rather than in the document, where it would have become a
	// schema of `{}` that every generated client reads as "any value".
	for _, td := range m.Types {
		for _, f := range td.Fields {
			if err := resolves(seen, td.ID, f.Name, f.Type); err != nil {
				return m, err
			}
		}
		if td.Elem != nil {
			if err := resolves(seen, td.ID, "element", *td.Elem); err != nil {
				return m, err
			}
		}
	}
	for _, op := range m.Ops {
		for what, id := range map[string]string{"input": op.In, "output": op.Out} {
			if id != "" && !seen[id] {
				return m, fmt.Errorf("op %s names %s %q, which nothing described", op.ID, what, id)
			}
		}
	}
	return m, nil
}

func resolves(seen map[string]bool, in, field string, r zip.TypeRef) error {
	switch {
	case r.Ref != "":
		if !seen[r.Ref] {
			return fmt.Errorf("%s.%s is %q, which nothing described", in, field, r.Ref)
		}
	case r.List != nil:
		return resolves(seen, in, field, *r.List)
	case r.Map != nil:
		return resolves(seen, in, field, *r.Map)
	}
	return nil
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
