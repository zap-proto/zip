// esbuild.go wraps esbuild's pure-Go API (no CGO) so zip can compile
// TS / modern-JS handler source down to ES5 that the embedded goja VM
// executes. This is the build step of zip's TS migration path:
//
//	TS source --esbuild target=es5--> ES5 JS --drop into--> embedded goja
//
// Run TranspileToES5 at service startup (or at build time) to compile
// bundled handlers before serving; the resulting bytes are handed to
// JSRuntime.LoadModule / Eval.
package runtime

import (
	"fmt"
	"path"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// ESOptions configures a transpile. Zero value is a sane default:
// TypeScript loader, ES5 target, no minification, CommonJS format so
// `module.exports = ...` survives into the goja-loadable output.
type ESOptions struct {
	// Loader selects how the source is parsed. "ts", "tsx", "jsx", or
	// "js". Empty defaults to "ts" (TS is a superset of JS, so plain JS
	// also parses).
	Loader string

	// Minify enables identifier/whitespace minification.
	Minify bool

	// Sourcefile is the logical filename used in error messages.
	Sourcefile string
}

// TranspileToES5 compiles src (TS or modern JS) to ES5 JavaScript ready
// for goja. The output is CommonJS-format so a module that does
// `module.exports = handler` can be loaded via JSRuntime.LoadModule and
// resolved with require(). Returns a non-nil error if esbuild reports
// any error-level diagnostic.
func TranspileToES5(src []byte, opts ESOptions) ([]byte, error) {
	loader := loaderFor(opts.Loader)
	sourcefile := opts.Sourcefile
	if sourcefile == "" {
		sourcefile = "handler" + extFor(loader)
	}

	result := api.Transform(string(src), api.TransformOptions{
		Loader:            loader,
		Target:            api.ES2015, // ES2015 is the lowest target esbuild emits; goja runs it.
		Format:            api.FormatCommonJS,
		Sourcefile:        sourcefile,
		MinifyWhitespace:  opts.Minify,
		MinifyIdentifiers: opts.Minify,
		MinifySyntax:      opts.Minify,
	})
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("zip/runtime: esbuild: %s", formatMessages(result.Errors))
	}
	return result.Code, nil
}

// BundleOptions configures a multi-file bundle. Zero value bundles to
// CommonJS (the format goja's require() understands) with no source map
// and no minification.
type BundleOptions struct {
	// Format is the output module format: "iife", "cjs", or "esm". Empty
	// defaults to "cjs", the format goja loads via require().
	Format string

	// Sourcemap inlines a base64 source map into the output for debugging.
	Sourcemap bool

	// Minify enables identifier/whitespace/syntax minification.
	Minify bool

	// External lists import paths (typically bare package specifiers like
	// "external" or "node:fs") that are left as require()/import calls in
	// the output instead of being bundled. Resolution of these is the
	// host's responsibility (e.g. a goja module registered under the same
	// name).
	External []string
}

// virtualNamespace tags resolved paths that live in the in-memory files
// map, so onLoad only fires for them and never touches the real FS.
const virtualNamespace = "zip-virtual"

// BundleToES2015 transpiles and bundles a multi-file TS/JS tree rooted at
// entry into a single ES2015 string ready for goja. files maps virtual
// paths (e.g. "entry.js", "lib/util.ts") to their source bytes; entry is
// the key of the root module. Relative imports between files are resolved
// against the importer's directory within the files map — no real
// filesystem access occurs. Packages listed in opts.External are left as
// require()/import calls. Returns a non-nil error if esbuild reports any
// error-level diagnostic (including an unresolved import).
func BundleToES2015(entry string, files map[string][]byte, opts BundleOptions) ([]byte, error) {
	if _, ok := files[entry]; !ok {
		return nil, fmt.Errorf("zip/runtime: bundle entry %q not in files map", entry)
	}
	entry = path.Clean(entry)

	external := map[string]struct{}{}
	for _, e := range opts.External {
		external[e] = struct{}{}
	}

	plugin := api.Plugin{
		Name: "zip-virtual-fs",
		Setup: func(build api.PluginBuild) {
			// Resolve: bare specifiers in External stay external; every
			// other import is mapped into the virtual namespace, with
			// relative paths joined against the importer's directory.
			build.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if _, ok := external[args.Path]; ok {
					return api.OnResolveResult{External: true}, nil
				}
				p := args.Path
				if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
					p = path.Join(path.Dir(args.Importer), p)
				}
				p = path.Clean(p)
				resolved, ok := resolveVirtual(p, files)
				if !ok {
					return api.OnResolveResult{}, fmt.Errorf("cannot resolve import %q from %q", args.Path, args.Importer)
				}
				return api.OnResolveResult{Path: resolved, Namespace: virtualNamespace}, nil
			})

			// Load: serve bytes from the files map, picking a loader from
			// the file extension.
			build.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: virtualNamespace}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				src, ok := files[args.Path]
				if !ok {
					return api.OnLoadResult{}, fmt.Errorf("virtual file %q disappeared", args.Path)
				}
				s := string(src)
				loader := loaderForPath(args.Path)
				return api.OnLoadResult{Contents: &s, Loader: loader}, nil
			})
		},
	}

	sourcemap := api.SourceMapNone
	if opts.Sourcemap {
		sourcemap = api.SourceMapInline
	}

	result := api.Build(api.BuildOptions{
		EntryPointsAdvanced: []api.EntryPoint{{InputPath: entry}},
		Bundle:              true,
		Write:               false,
		Format:              bundleFormat(opts.Format),
		Target:              api.ES2015,
		Sourcemap:           sourcemap,
		MinifyWhitespace:    opts.Minify,
		MinifyIdentifiers:   opts.Minify,
		MinifySyntax:        opts.Minify,
		Plugins:             []api.Plugin{plugin},
	})
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("zip/runtime: esbuild bundle: %s", formatMessages(result.Errors))
	}
	if len(result.OutputFiles) == 0 {
		return nil, fmt.Errorf("zip/runtime: esbuild bundle produced no output")
	}
	return result.OutputFiles[0].Contents, nil
}

// resolveVirtual finds p in files, trying p as-is then with the common
// JS/TS extensions appended (so `./util` resolves to `util.ts` etc.).
func resolveVirtual(p string, files map[string][]byte) (string, bool) {
	if _, ok := files[p]; ok {
		return p, true
	}
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".json"} {
		if _, ok := files[p+ext]; ok {
			return p + ext, true
		}
	}
	return "", false
}

// loaderForPath selects an esbuild loader from a virtual path's extension.
func loaderForPath(p string) api.Loader {
	switch path.Ext(p) {
	case ".ts":
		return api.LoaderTS
	case ".tsx":
		return api.LoaderTSX
	case ".jsx":
		return api.LoaderJSX
	case ".json":
		return api.LoaderJSON
	default:
		return api.LoaderJS
	}
}

// bundleFormat maps a BundleOptions.Format string to an esbuild format,
// defaulting to CommonJS (goja's require() format).
func bundleFormat(name string) api.Format {
	switch name {
	case "iife":
		return api.FormatIIFE
	case "esm":
		return api.FormatESModule
	case "", "cjs":
		return api.FormatCommonJS
	default:
		return api.FormatCommonJS
	}
}

func loaderFor(name string) api.Loader {
	switch name {
	case "", "ts":
		return api.LoaderTS
	case "tsx":
		return api.LoaderTSX
	case "jsx":
		return api.LoaderJSX
	case "js":
		return api.LoaderJS
	default:
		return api.LoaderTS
	}
}

func extFor(l api.Loader) string {
	switch l {
	case api.LoaderTSX:
		return ".tsx"
	case api.LoaderJSX, api.LoaderJS:
		return ".js"
	default:
		return ".ts"
	}
}

func formatMessages(msgs []api.Message) string {
	out := ""
	for i, m := range msgs {
		if i > 0 {
			out += "; "
		}
		out += m.Text
		if m.Location != nil {
			out += fmt.Sprintf(" (%s:%d:%d)", m.Location.File, m.Location.Line, m.Location.Column)
		}
	}
	return out
}
