// Command zipgen generates native Go, Rust, and C++ SDKs, MCP servers, and @hanzo/docs.
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

Run 'zipgen <command> -h' for command options.
`)
}

func runSDK(args []string) {
	fs := flag.NewFlagSet("sdk", flag.ExitOnError)
	lang := fs.String("lang", "rust", "target language: rust, cpp, or go")
	pkg := fs.String("pkg", "client", "package/crate/namespace name")
	out := fs.String("o", "", "output directory or file")
	fs.Parse(args)

	app := zip.New(zip.Config{AppName: *pkg})
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

func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	lang := fs.String("lang", "rust", "target language: rust, cpp, or go")
	pkg := fs.String("pkg", "mcp", "package/crate/namespace name")
	out := fs.String("o", "", "output directory or file")
	fs.Parse(args)

	app := zip.New(zip.Config{AppName: *pkg})
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
	lang := fs.String("lang", "rust", "target language: rust, cpp, or go")
	pkg := fs.String("pkg", "cli", "package/crate/namespace name")
	out := fs.String("o", "", "output directory or file")
	fs.Parse(args)

	app := zip.New(zip.Config{AppName: *pkg})
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
	title := fs.String("title", "API Reference", "documentation title")
	out := fs.String("o", "./docs", "output directory for MDX pages")
	fs.Parse(args)

	app := zip.New(zip.Config{AppName: *title})
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
