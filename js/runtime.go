// Package runtime is zip's in-tree JavaScript extension runtime: goja to
// evaluate and esbuild to bundle/transpile. It is a SEPARATE import from
// the root zip package on purpose — a service that never evaluates JS must
// not compile a JS interpreter and bundler, so the root declares only the
// [github.com/zap-proto/zip.Loader] contract and this package is opted into
// by the binaries that want JS:
//
//	rt, _ := runtime.NewJSRuntime(runtime.JSOptions{PoolSize: 8})
//	app := zip.New(zip.Config{Loader: myLoader}) // Loader is zip.Loader
//
// The Loader/Module contract app.Module() consumes lives at the zip root
// ([github.com/zap-proto/zip.Loader], [github.com/zap-proto/zip.Module]) —
// it is duck-typed, so an implementation registers itself by being passed
// in, never by zip importing it.
package js
