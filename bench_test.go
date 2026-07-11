package zip_test

// Framework-tax benchmarks for zip over its underlying Fiber v3 fork.
//
// Two costs are isolated here, each against a hand-written baseline so the
// number is the *tax*, not the absolute:
//
//  1. Per-request wrapper tax (Benchmark_ZipTax): zip.App vs a raw fiber.App
//     serving the identical route with an equivalent minimal handler. The delta
//     is what zip's Handler wrapper costs per request — one &Ctx heap value
//     (fc, app, log) materialised by toFiberHandler, plus the indirection.
//
//  2. Typed-route tax (Benchmark_TypedRoute): the generic Post[In,Out] (decode →
//     validate → run → encode, transport-agnostic op.invoke core) vs an
//     idiomatic hand-written zip handler doing c.Bind + c.JSON. The delta is
//     what the generic sugar (and the OpenAPI/MCP registry it feeds) costs at
//     request time.
//
// Both dispatch through app.Fiber().Handler() on a reused *fasthttp.RequestCtx —
// the low-overhead router idiom, so the framework tax isn't buried under
// net/http↔fasthttp conversion (as it is in the app.Test path used by
// json_bench_test.go). chatRequest / chatResponse / benchReqBody are shared with
// json_bench_test.go (same package).
//
// Run:
//
//	go test -run='^$' -bench='Benchmark_ZipTax|Benchmark_TypedRoute' -benchmem -count=6 .
//
// Numbers and methodology: BENCHMARKS.md.

import (
	"context"
	"testing"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip"
)

// zipTaxRoutes covers the routing shapes a wrapper tax should be constant
// across: a static route and a single-param route.
var zipTaxRoutes = []struct {
	name   string
	route  string // registration pattern
	path   string // concrete request path
}{
	{"static", "/v1/health", "/v1/health"},
	{"param", "/v1/tracker/:id", "/v1/tracker/trk_abc123"},
}

func benchConfig() zip.Config {
	return zip.Config{DisableStartupMessage: true, ServerHeader: "-"}
}

// getFctx builds a reusable GET request context.
func getFctx(path string) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.URI().SetPath(path)
	return fctx
}

// Benchmark_ZipTax measures zip.App's per-request overhead against a raw
// fiber.App. Handlers are equivalent to the byte: zip's NoContent(204) is
// c.fc.Status(204)+return nil; the fiber baseline is c.Status(204)+return nil.
// The only difference under test is zip's wrapper (the &Ctx materialised per
// request). Expect ~constant delta across routing shapes.
func Benchmark_ZipTax(b *testing.B) {
	za := zip.New(benchConfig())
	fa := fiber.New()
	for _, r := range zipTaxRoutes {
		za.Get(r.route, func(c *zip.Ctx) error { return c.NoContent(204) })
		fa.Get(r.route, func(c fiber.Ctx) error { c.Status(204); return nil })
	}
	zh := za.Fiber().Handler()
	fh := fa.Handler()

	for _, r := range zipTaxRoutes {
		fctx := getFctx(r.path)
		b.Run(r.name+"/zip", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				zh(fctx)
			}
			if fctx.Response.StatusCode() != 204 {
				b.Fatalf("zip: status %d", fctx.Response.StatusCode())
			}
		})
		b.Run(r.name+"/fiber", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				fh(fctx)
			}
			if fctx.Response.StatusCode() != 204 {
				b.Fatalf("fiber: status %d", fctx.Response.StatusCode())
			}
		})
	}
}

// postFctx builds a reusable POST request context carrying a JSON body.
func postFctx(path string, body []byte) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("POST")
	fctx.Request.SetRequestURI(path)
	fctx.Request.Header.SetContentType("application/json")
	fctx.Request.SetBody(body)
	return fctx
}

// makeChatResponse builds the response both handler variants return, so the
// encode side of the comparison is identical.
func makeChatResponse(model string) chatResponse {
	return chatResponse{
		ID:               "chatcmpl-bench",
		Model:            model,
		Content:          "Cars are flying. Wire stack: ingress→gateway→subsystem; JSON at edge, ZAP between.",
		PromptTokens:     27,
		CompletionTokens: 19,
		TotalTokens:      46,
		FinishReason:     "stop",
		Latency:          42.5,
	}
}

// Benchmark_TypedRoute compares the generic Post[In,Out] against an idiomatic
// hand-written zip handler doing the same decode → build → encode work. The
// delta is the generic wrapper's per-request tax (the op.invoke indirection and
// the any-typed result the OpenAPI/MCP projections require).
func Benchmark_TypedRoute(b *testing.B) {
	// Generic typed route.
	ta := zip.New(benchConfig())
	zip.Post[chatRequest, chatResponse](ta, "/v1/chat",
		func(_ context.Context, in *chatRequest) (*chatResponse, error) {
			out := makeChatResponse(in.Model)
			return &out, nil
		})
	th := ta.Fiber().Handler()

	// Hand-written zip handler — same work, no generics.
	ha := zip.New(benchConfig())
	ha.Post("/v1/chat", func(c *zip.Ctx) error {
		var in chatRequest
		if err := c.Bind(&in); err != nil {
			return err
		}
		out := makeChatResponse(in.Model)
		return c.JSON(200, &out)
	})
	hh := ha.Fiber().Handler()

	b.Run("typed", func(b *testing.B) {
		fctx := postFctx("/v1/chat", benchReqBody)
		b.ReportAllocs()
		for b.Loop() {
			th(fctx)
		}
		if sc := fctx.Response.StatusCode(); sc != 200 {
			b.Fatalf("typed: status %d", sc)
		}
	})
	b.Run("handwritten", func(b *testing.B) {
		fctx := postFctx("/v1/chat", benchReqBody)
		b.ReportAllocs()
		for b.Loop() {
			hh(fctx)
		}
		if sc := fctx.Response.StatusCode(); sc != 200 {
			b.Fatalf("handwritten: status %d", sc)
		}
	})
}
