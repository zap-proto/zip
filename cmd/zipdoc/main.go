// Command zipdoc makes a doc comment the spec.
//
// It walks a package for typed registrations — zip.Get[In,Out](app, path, fn)
// and its Post/Put/Patch/Delete siblings — takes the doc comment on each handler
// and on each field of its In and Out types, and writes a file that hands them
// to zip.Describe at init. From there they are the operation's description, its
// fields' descriptions and its request/response examples in the OpenAPI spec
// zip serves, and in the MCP tools it exposes.
//
// The point is that nothing is written twice. A summary passed to WithSummary
// sitting under a doc comment that says the same thing is two places to change
// and one to forget; this pass removes the second place.
//
// Put this above the package's route registrations:
//
//	//go:generate zipdoc
//
// and write handlers the way Go already asks you to:
//
//	// ListInvoices returns every invoice for the caller's org, newest first.
//	//
//	// Example: {"org": "hanzo", "limit": 25}
//	// Response: {"invoices": [{"id": "inv_1", "cents": 1200}]}
//	func ListInvoices(ctx context.Context, in *ListIn) (*ListOut, error)
//
// A malformed Example or Response fails generation rather than shipping a spec
// that is wrong.
//
// Usage:
//
//	zipdoc [-o name] [-check] [packages]
//
// With -check nothing is written and a file that no longer matches its source is
// an error. That is the CI gate: the spec is derived from the code, so it cannot
// drift from it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zap-proto/zip/internal/zipdoc"
)

func main() {
	name := flag.String("o", zipdoc.DefaultFile, "name of the generated file, written in each package's own directory")
	check := flag.Bool("check", false, "write nothing; fail if a generated file is out of date")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: zipdoc [-o name] [-check] [packages]\n\n"+
			"Extracts the doc comments on zip's typed handlers into zip.Describe calls.\n"+
			"Packages default to the current directory, as `go generate` runs it.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := run(*name, *check, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "zipdoc:", err)
		os.Exit(1)
	}
}

func run(name string, check bool, patterns []string) error {
	if filepath.Base(name) != name {
		return fmt.Errorf("-o takes a file name, not a path: %s", name)
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	pkgs, err := zipdoc.Load(dir, patterns)
	if err != nil {
		return err
	}
	var stale []string
	for _, p := range pkgs {
		if check {
			why, err := p.Stale(name)
			if err != nil {
				return err
			}
			if why != "" {
				stale = append(stale, why)
			}
			continue
		}
		changed, err := p.Write(name)
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintln(os.Stderr, "zipdoc:", filepath.Join(p.Dir, name))
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("%d generated file(s) do not match the source; run `go generate ./...`:\n\t%s",
			len(stale), strings.Join(stale, "\n\t"))
	}
	return nil
}
