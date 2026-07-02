package runtime

import (
	"strings"
	"testing"
)

// A two-file CommonJS tree (entry.js requires ./util.js) bundles to a
// single ES2015 string that runs in goja and produces the expected value.
func TestBundleToES2015_TwoFileCommonJS(t *testing.T) {
	files := map[string][]byte{
		"util.js": []byte(`
			module.exports.triple = function (n) { return n * 3; };
		`),
		"entry.js": []byte(`
			const util = require("./util.js");
			module.exports.run = function () { return util.triple(14); };
		`),
	}

	out, err := BundleToES2015("entry.js", files, BundleOptions{Format: "cjs"})
	if err != nil {
		t.Fatal(err)
	}

	rt, _ := NewJSRuntime(JSOptions{PoolSize: 1})
	if err := rt.LoadModule("bundle", string(out)); err != nil {
		t.Fatal(err)
	}
	v, err := rt.Eval(`require("bundle").run()`)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.(int64); !ok || got != 42 {
		t.Fatalf("entry.run() = %v (%T), want int64(42)", v, v)
	}
	t.Logf("two-file bundle: entry.run() == %d (want 42)", v)
}

// TypeScript across files bundles and strips types.
func TestBundleToES2015_TypeScriptTree(t *testing.T) {
	files := map[string][]byte{
		"math.ts": []byte(`export const square = (n: number): number => n * n;`),
		"entry.ts": []byte(`
			import { square } from "./math";
			module.exports.run = (): number => square(7);
		`),
	}
	out, err := BundleToES2015("entry.ts", files, BundleOptions{Format: "cjs"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ": number") {
		t.Fatalf("types not stripped: %s", out)
	}

	rt, _ := NewJSRuntime(JSOptions{PoolSize: 1})
	if err := rt.LoadModule("b", string(out)); err != nil {
		t.Fatal(err)
	}
	v, err := rt.Eval(`require("b").run()`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(int64); got != 49 {
		t.Fatalf("run() = %d, want 49", got)
	}
}

// An external dependency must stay as a require("external") call in the
// output rather than being inlined (it is not in the files map).
func TestBundleToES2015_ExternalStaysRequire(t *testing.T) {
	files := map[string][]byte{
		"entry.js": []byte(`
			const ext = require("external");
			module.exports.run = function () { return ext.value; };
		`),
	}

	out, err := BundleToES2015("entry.js", files, BundleOptions{
		Format:   "cjs",
		External: []string{"external"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `require("external")`) {
		t.Fatalf("external not preserved as require: %s", out)
	}
	t.Logf(`external preserved: output contains require("external")`)

	// And it must actually resolve through goja's require() to the host
	// module registered under the same name.
	rt, _ := NewJSRuntime(JSOptions{
		PoolSize: 1,
		Modules:  map[string]string{"external": `module.exports = { value: 99 };`},
	})
	if err := rt.LoadModule("bundle", string(out)); err != nil {
		t.Fatal(err)
	}
	v, err := rt.Eval(`require("bundle").run()`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(int64); got != 99 {
		t.Fatalf("run() = %d, want 99 (from external module)", got)
	}
}

// A missing entry is a clear error, not a panic.
func TestBundleToES2015_MissingEntry(t *testing.T) {
	_, err := BundleToES2015("nope.js", map[string][]byte{"a.js": []byte("1")}, BundleOptions{})
	if err == nil {
		t.Fatal("expected error for missing entry, got nil")
	}
}

// An unresolved relative import is reported as an error.
func TestBundleToES2015_UnresolvedImport(t *testing.T) {
	files := map[string][]byte{
		"entry.js": []byte(`require("./missing.js");`),
	}
	_, err := BundleToES2015("entry.js", files, BundleOptions{})
	if err == nil {
		t.Fatal("expected error for unresolved import, got nil")
	}
}
