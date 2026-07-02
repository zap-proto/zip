// handler.go bridges a goja-loaded JS function into Fiber. It returns
// fiber.Handler (not zip.Handler) on purpose: the zip root package
// imports this package, so depending back on zip would be an import
// cycle. fiber.Handler mounts cleanly anywhere — app.Fiber().Get(...)
// or any *fiber.App — and is the natural seam for legacy Express-shaped
// handlers running inside the embedded VM.
//
// The req/res objects handed to JS mirror the Express shape so existing
// `function (req, res) { res.json(...) }` handlers run unmodified:
//
//	req.method   req.path   req.query   req.headers   req.body
//	res.status(n)   res.set(k,v)   res.json(v)   res.send(v)
package runtime

import (
	"fmt"

	"github.com/dop251/goja"
	"github.com/gofiber/fiber/v3"
)

// jsResponse captures what the JS handler wrote, so we can replay it onto
// the Fiber Ctx after the VM call returns. goja runs single-threaded per
// borrowed VM, so a fresh jsResponse per request is race-free.
type jsResponse struct {
	status  int
	headers map[string]string
	body    []byte
	isJSON  bool
	written bool
}

// JSHandler returns a fiber.Handler that invokes the global JS function
// named fnName (defined in the runtime via Eval / LoadModule) with an
// Express-shaped (req, res) pair. State written through res is propagated
// back onto the Fiber Ctx after the call.
func JSHandler(rt *JSRuntime, fnName string) fiber.Handler {
	return func(fc fiber.Ctx) error {
		return rt.withVM(func(vm *goja.Runtime) error {
			fnVal := vm.Get(fnName)
			fn, ok := goja.AssertFunction(fnVal)
			if !ok {
				return fmt.Errorf("zip/runtime: %q is not a callable JS function", fnName)
			}
			return invokeJS(fc, vm, fn)
		})
	}
}

// JSModule loads a CommonJS module whose `module.exports` is the handler
// function (Express shape) and returns a fiber.Handler for it. modulePath
// is the name the module was registered under via LoadModule.
func JSModule(rt *JSRuntime, modulePath string) (fiber.Handler, error) {
	// Resolve once up front so a missing/invalid module fails at mount
	// time, not on the first request.
	var resolved goja.Callable
	err := rt.withVM(func(vm *goja.Runtime) error {
		exports, err := requireModule(vm, modulePath)
		if err != nil {
			return err
		}
		if _, ok := goja.AssertFunction(exports); !ok {
			return fmt.Errorf("zip/runtime: module %q exports is not a function", modulePath)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	_ = resolved // resolution is per-VM; re-resolved inside the handler.

	return func(fc fiber.Ctx) error {
		return rt.withVM(func(vm *goja.Runtime) error {
			exports, err := requireModule(vm, modulePath)
			if err != nil {
				return err
			}
			fn, ok := goja.AssertFunction(exports)
			if !ok {
				return fmt.Errorf("zip/runtime: module %q exports is not a function", modulePath)
			}
			return invokeJS(fc, vm, fn)
		})
	}, nil
}

// invokeJS builds the req/res objects, calls fn(req, res), then replays
// the captured response onto the Fiber Ctx.
func invokeJS(fc fiber.Ctx, vm *goja.Runtime, fn goja.Callable) error {
	resp := &jsResponse{status: 200, headers: map[string]string{}}

	req := buildReq(vm, fc)
	res := buildRes(vm, resp)

	if _, err := fn(goja.Undefined(), req, res); err != nil {
		return err
	}
	return replay(fc, resp)
}

// buildReq materialises the Express-shaped req object from the Fiber Ctx.
func buildReq(vm *goja.Runtime, fc fiber.Ctx) *goja.Object {
	req := vm.NewObject()
	_ = req.Set("method", fc.Method())
	_ = req.Set("path", fc.Path())
	_ = req.Set("url", fc.OriginalURL())

	// query: flat map of first values.
	query := vm.NewObject()
	for k, v := range fc.Queries() {
		_ = query.Set(k, v)
	}
	_ = req.Set("query", query)

	// headers: lowercased keys, Express convention.
	headers := vm.NewObject()
	for k, v := range fc.GetReqHeaders() {
		if len(v) > 0 {
			_ = headers.Set(k, v[0])
		}
	}
	_ = req.Set("headers", headers)

	// body: parsed JSON when Content-Type is JSON and the body is
	// non-empty, otherwise null. Matches Express + body-parser.
	body := fc.Body()
	if len(body) > 0 && isJSONContentType(fc.Get(fiber.HeaderContentType)) {
		var parsed any
		if err := jsonUnmarshal(body, &parsed); err == nil {
			_ = req.Set("body", vm.ToValue(parsed))
		} else {
			_ = req.Set("body", goja.Null())
		}
	} else {
		_ = req.Set("body", goja.Null())
	}
	return req
}

// buildRes materialises the Express-shaped res object backed by resp.
func buildRes(vm *goja.Runtime, resp *jsResponse) *goja.Object {
	res := vm.NewObject()

	// res.status(n) -> res  (chainable)
	_ = res.Set("status", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 {
			resp.status = int(call.Argument(0).ToInteger())
		}
		return res
	})

	// res.set(k, v) -> res  (chainable)
	_ = res.Set("set", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) >= 2 {
			resp.headers[call.Argument(0).String()] = call.Argument(1).String()
		}
		return res
	})

	// res.json(v) — serialize v as JSON, set Content-Type.
	_ = res.Set("json", func(call goja.FunctionCall) goja.Value {
		var v any
		if len(call.Arguments) > 0 {
			v = call.Argument(0).Export()
		}
		b, err := jsonMarshal(v)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		resp.body = b
		resp.isJSON = true
		resp.written = true
		return res
	})

	// res.send(v) — string/bytes body, no forced Content-Type.
	_ = res.Set("send", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 {
			arg := call.Argument(0)
			if b, ok := arg.Export().([]byte); ok {
				resp.body = b
			} else {
				resp.body = []byte(arg.String())
			}
		}
		resp.written = true
		return res
	})

	return res
}

// replay writes the captured JS response onto the Fiber Ctx.
func replay(fc fiber.Ctx, resp *jsResponse) error {
	for k, v := range resp.headers {
		fc.Set(k, v)
	}
	fc.Status(resp.status)
	if resp.isJSON {
		fc.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	if !resp.written || len(resp.body) == 0 {
		return nil
	}
	return fc.Send(resp.body)
}

func isJSONContentType(ct string) bool {
	for i := 0; i+4 <= len(ct); i++ {
		if ct[i:i+4] == "json" {
			return true
		}
	}
	return false
}
