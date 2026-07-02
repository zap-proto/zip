package runtime

import (
	"fmt"
	"sync"

	"github.com/dop251/goja"
)

// CommonJS-style module loading without an external dependency. Each VM
// gets a require(name) global backed by a per-VM registry of module
// sources and a memoized cache of evaluated module.exports. A module is
// evaluated the first time it is required, wrapped in the canonical
// CommonJS function scope:
//
//	(function (module, exports, require) { <src> })
//
// so module source can do `module.exports = ...` or `exports.foo = ...`.

const requireRegistryKey = "__zip_modules__"
const requireCacheKey = "__zip_module_cache__"

// vmModules holds a VM's module sources and evaluated-exports cache. It
// lives in a Go-side side-table keyed by the *goja.Runtime pointer so it
// is never visible to (or mutable from) JS.
type vmModules struct {
	mu      sync.Mutex
	sources map[string]string
	cache   map[string]goja.Value
}

var (
	moduleTablesMu sync.Mutex
	moduleTables   = map[*goja.Runtime]*vmModules{}
)

func tableFor(vm *goja.Runtime) *vmModules {
	moduleTablesMu.Lock()
	defer moduleTablesMu.Unlock()
	t, ok := moduleTables[vm]
	if !ok {
		t = &vmModules{sources: map[string]string{}, cache: map[string]goja.Value{}}
		moduleTables[vm] = t
	}
	return t
}

// installRequire registers the require() global on vm and seeds it with
// the given module sources.
func installRequire(vm *goja.Runtime, modules map[string]string) error {
	t := tableFor(vm)
	t.mu.Lock()
	for name, src := range modules {
		t.sources[name] = src
	}
	t.mu.Unlock()

	return vm.Set("require", func(name string) (goja.Value, error) {
		return requireModule(vm, name)
	})
}

// registerModule adds (or replaces) one module source on vm and
// invalidates any cached evaluation of it.
func registerModule(vm *goja.Runtime, name, src string) error {
	t := tableFor(vm)
	t.mu.Lock()
	t.sources[name] = src
	delete(t.cache, name)
	t.mu.Unlock()
	// Ensure require() exists even if the VM was built with no modules.
	if v := vm.Get("require"); v == nil || goja.IsUndefined(v) {
		return vm.Set("require", func(name string) (goja.Value, error) {
			return requireModule(vm, name)
		})
	}
	return nil
}

// requireModule resolves name to its module.exports, evaluating the
// source on first use and memoizing the result.
func requireModule(vm *goja.Runtime, name string) (goja.Value, error) {
	t := tableFor(vm)
	t.mu.Lock()
	if cached, ok := t.cache[name]; ok {
		t.mu.Unlock()
		return cached, nil
	}
	src, ok := t.sources[name]
	t.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("zip/runtime: module not found: %q", name)
	}

	// Wrap in the CommonJS scope and evaluate.
	wrapped := "(function(module, exports, require){" + src + "\n})"
	fnVal, err := vm.RunString(wrapped)
	if err != nil {
		return nil, fmt.Errorf("zip/runtime: load module %q: %w", name, err)
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return nil, fmt.Errorf("zip/runtime: module %q did not compile to a function", name)
	}

	module := vm.NewObject()
	exports := vm.NewObject()
	_ = module.Set("exports", exports)
	requireFn := vm.Get("require")

	if _, err := fn(goja.Undefined(), module, exports, requireFn); err != nil {
		return nil, fmt.Errorf("zip/runtime: eval module %q: %w", name, err)
	}

	result := module.Get("exports")
	t.mu.Lock()
	t.cache[name] = result
	t.mu.Unlock()
	return result, nil
}
