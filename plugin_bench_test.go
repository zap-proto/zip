package zip_test

// What a plugin costs per request.
//
// Running a service as its own binary buys independent build, release, restart
// and measurement — and charges a hop for it. This benchmark prices that hop by
// serving the SAME route two ways through the same host: once by a handler
// linked into the host, once by a child process the host reaches over its unix
// socket. Everything else is held equal, so the delta IS the hop.
//
// It exists because the trade is only decidable with the number in hand. A hop
// that is small against a real handler's work is a deployment decision; one that
// is large is an architecture constraint, and the two look identical in prose.
//
// Run one round per process and take the minimum across rounds, so A and B
// interleave under drifting load instead of A running entirely before B:
//
//	for i in $(seq 8); do
//	  go test -run='^$' -bench=Benchmark_PluginHop -benchmem -count=1 -benchtime=3000x .
//	done
//
// Numbers and methodology: BENCHMARKS.md §3.

import (
	"testing"

	"github.com/zap-proto/zip"
)

// Benchmark_PluginHop compares a linked-in route against the same route served
// by a loaded plugin. Both dispatch through app.Fiber().Handler() on a reused
// *fasthttp.RequestCtx, the same idiom as Benchmark_ZipTax, so neither side pays
// net/http↔fasthttp conversion.
func Benchmark_PluginHop(b *testing.B) {
	bin := buildPlugin(b, "v1")

	// The linked-in baseline answers byte-for-byte what the plugin answers, so
	// the only difference under test is where the handler runs.
	linked := zip.New(benchConfig())
	linked.Get("/v1/demo/version", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"version": "v1"})
	})

	host := zip.New(benchConfig())
	host.Use(must(zip.Load(zip.Plugin{Name: "demo", Bin: bin}, "/v1/demo")))
	defer func() { _ = host.Shutdown() }()

	for _, tc := range []struct {
		name string
		app  *zip.App
	}{{"linked", linked}, {"plugin", host}} {
		h := tc.app.Fiber().Handler()
		b.Run(tc.name, func(b *testing.B) {
			fctx := getFctx("/v1/demo/version")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fctx.ResetUserValues()
				h(fctx)
			}
		})
	}
}
