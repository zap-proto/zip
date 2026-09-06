// Command zipgen generates native Go, Rust, and C++ SDKs, MCP servers, and @hanzo/docs.
//
// Every projection here is computed from an app's typed-op registry, which
// is the source: the ops decide the routes, the OpenAPI document, the MCP
// tool list, the CLI and the docs pages, and nothing downstream is written
// by hand. An app with no ops therefore has no projection — see [source].
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zap-proto/zip"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "sdk":
		runSDK(args)
	case "mcp":
		runMCP(args)
	case "cli":
		runCLI(args)
	case "docs":
		runDocs(args)
	case "zap":
		runZap(args)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: zipgen <command> [options]

Commands:
  sdk    Generate native Go, Rust, or C++ client SDK with full doc comments
  mcp    Generate native Go, Rust, or C++ MCP server and tools
  cli    Generate native Go, Rust, or C++ CLI commands and runners
  docs   Generate @hanzo/docs compatible MDX documentation pages
  zap    Generate the ZAP wire runtime (reader + builder) for a target language

Run 'zipgen <command> -h' for command options.
`)
}

func runSDK(args []string) {
	fs := flag.NewFlagSet("sdk", flag.ExitOnError)
	schema := fs.String("schema", "", "path to the .zap schema declaring the ops to project")
	iface := fs.String("interface", "", "which interface of the schema to project (required when it declares more than one)")
	lang := fs.String("lang", "rust", "target language: rust, cpp, or go")
	pkg := fs.String("pkg", "client", "package/crate/namespace name")
	out := fs.String("o", "", "output directory or file")
	fs.Parse(args)

	app := source(*schema, *iface, "an SDK")
	switch strings.ToLower(*lang) {
	case "rust", "rs":
		res, err := app.RustSDK(*pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
			os.Exit(1)
		}
		writeOutput(*out, "lib.rs", res.Source)
	case "cpp", "c++":
		res, err := app.CppSDK(*pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
			os.Exit(1)
		}
		writeOutput(*out, "client.hpp", res.Header)
	case "go":
		res, err := app.SDK(*pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
			os.Exit(1)
		}
		writeOutput(*out, "client.go", res.Source)
	default:
		fmt.Fprintf(os.Stderr, "unknown language: %s (choose rust, cpp, or go)\n", *lang)
		os.Exit(1)
	}
}

// runZap emits the ZAP wire runtime itself — the reader and the builder — for a
// target language. This is the codec every generated SDK and every hand-written
// chain reads and writes bytes with, so it is stated once here rather than
// ported per repository.
func runZap(args []string) {
	fs := flag.NewFlagSet("zap", flag.ExitOnError)
	lang := fs.String("lang", "cpp", "target language: cpp")
	ns := fs.String("ns", "lux::zap", "namespace for the emitted runtime")
	out := fs.String("o", "", "output directory or file")
	fs.Parse(args)

	switch strings.ToLower(*lang) {
	case "cpp", "c++":
		writeOutput(*out, "zap.hpp", zip.CppZap(*ns))
	default:
		fmt.Fprintf(os.Stderr, "unknown language: %s (choose cpp)\n", *lang)
		os.Exit(1)
	}
}

func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	schema := fs.String("schema", "", "path to the .zap schema declaring the ops to project")
	iface := fs.String("interface", "", "which interface of the schema to project (required when it declares more than one)")
	lang := fs.String("lang", "rust", "target language: rust, cpp, or go")
	pkg := fs.String("pkg", "mcp", "package/crate/namespace name")
	out := fs.String("o", "", "output directory or file")
	fs.Parse(args)

	app := source(*schema, *iface, "an MCP server")
	switch strings.ToLower(*lang) {
	case "rust", "rs":
		res, err := app.RustMCP(*pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
			os.Exit(1)
		}
		writeOutput(*out, "mcp.rs", res.Source)
	case "cpp", "c++":
		res, err := app.CppMCP(*pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
			os.Exit(1)
		}
		writeOutput(*out, "mcp.hpp", res.Header)
	default:
		fmt.Fprintf(os.Stderr, "unknown language: %s (choose rust or cpp)\n", *lang)
		os.Exit(1)
	}
}

func runCLI(args []string) {
	fs := flag.NewFlagSet("cli", flag.ExitOnError)
	schema := fs.String("schema", "", "path to the .zap schema declaring the ops to project")
	iface := fs.String("interface", "", "which interface of the schema to project (required when it declares more than one)")
	lang := fs.String("lang", "rust", "target language: rust, cpp, or go")
	pkg := fs.String("pkg", "cli", "package/crate/namespace name")
	out := fs.String("o", "", "output directory or file")
	fs.Parse(args)

	app := source(*schema, *iface, "a CLI")
	switch strings.ToLower(*lang) {
	case "rust", "rs":
		res, err := app.RustCLI(*pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
			os.Exit(1)
		}
		writeOutput(*out, "cli.rs", res.Source)
	case "cpp", "c++":
		res, err := app.CppCLI(*pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
			os.Exit(1)
		}
		writeOutput(*out, "cli.hpp", res.Header)
	case "go":
		cmds := app.Commands()
		data, _ := json.MarshalIndent(cmds, "", "  ")
		writeOutput(*out, "cli.json", data)
	default:
		fmt.Fprintf(os.Stderr, "unknown language: %s (choose rust, cpp, or go)\n", *lang)
		os.Exit(1)
	}
}

func runDocs(args []string) {
	fs := flag.NewFlagSet("docs", flag.ExitOnError)
	schema := fs.String("schema", "", "path to the .zap schema declaring the ops to project")
	iface := fs.String("interface", "", "which interface of the schema to project (required when it declares more than one)")
	title := fs.String("title", "API Reference", "documentation title")
	out := fs.String("o", "./docs", "output directory for MDX pages")
	fs.Parse(args)

	app := source(*schema, *iface, "documentation")
	bundle, err := app.DocsMarkdown(*title)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(*out, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
		os.Exit(1)
	}
	_ = os.WriteFile(filepath.Join(*out, "index.mdx"), []byte(bundle.Index), 0644)
	for fname, content := range bundle.Pages {
		_ = os.WriteFile(filepath.Join(*out, fname), []byte(content), 0644)
	}
	fmt.Printf("Generated %d documentation pages in %s\n", len(bundle.Pages)+1, *out)
}

// source is the app whose registry the projections are computed from, read
// from the schema that declares it.
//
// zipgen used to build its app from flags alone, so the registry was empty and
// every projection of it was empty too: an SDK with no methods, an MCP server
// with no tools, a docs page whose operations table has no rows, a CLI spec
// that is the two bytes "[]". Each of those was written out and reported as a
// success, which is worse than writing nothing — a plausible-looking file that
// a reader takes for the API, whose emptiness is only visible to someone who
// counts.
//
// The registry now comes from a .zap schema, which is where a service that is
// not this process states its contract. zip.ReadZAP parses it with the same
// package cmd/zapgen parses it with and derives one op per method; from there
// every projection is the one it always was. A run with no schema still has
// nothing to see, and still says so.
//
// A schema declares one service per interface and may declare several, so which
// one to project is named rather than assumed. Only a file with exactly one can
// leave it out.
func source(schema, want, what string) *zip.App {
	if schema == "" {
		fmt.Fprintf(os.Stderr, `zipgen: cannot generate %s — no operations to project.

Name the .zap schema that declares them:

    zipgen ... -schema path/to/service.zap

Without one this command has an empty registry, and every projection of an
empty registry is empty. Emitting one would publish a file that looks like an
API and describes nothing.
`, what)
		os.Exit(1)
	}
	src, err := os.ReadFile(schema)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
		os.Exit(1)
	}
	apps, err := zip.ReadZAP(schema, src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
		os.Exit(1)
	}
	app := choose(apps, want, schema)
	// A schema whose interface declares no methods parses, and projects nothing.
	// The read above accounts for every method it could not express, so reaching
	// here with an empty registry means the interface was empty.
	if len(app.Registry()) == 0 {
		fmt.Fprintf(os.Stderr, "zipgen: cannot generate %s — interface %s declares no methods\n", what, app.Name())
		os.Exit(1)
	}
	return app
}

// choose is the service to project, by name, or the only one there is.
func choose(apps []*zip.App, want, schema string) *zip.App {
	names := make([]string, len(apps))
	for i, a := range apps {
		names[i] = a.Name()
		if a.Name() == want {
			return a
		}
	}
	switch {
	case want != "":
		fmt.Fprintf(os.Stderr, "zipgen: %s declares no interface %s; it declares %s\n",
			schema, want, strings.Join(names, ", "))
	case len(apps) == 1:
		return apps[0]
	default:
		fmt.Fprintf(os.Stderr, "zipgen: %s declares %d interfaces (%s); name one with -interface\n",
			schema, len(apps), strings.Join(names, ", "))
	}
	os.Exit(1)
	return nil
}

func writeOutput(outPath, defaultName string, data []byte) {
	if outPath == "" || outPath == "-" {
		os.Stdout.Write(data)
		return
	}
	stat, err := os.Stat(outPath)
	target := outPath
	if err == nil && stat.IsDir() {
		target = filepath.Join(outPath, defaultName)
	}
	if err := os.WriteFile(target, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "zipgen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s (%d bytes)\n", target, len(data))
}
