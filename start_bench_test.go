package zip_test

// What a lazy plugin's FIRST request costs: start the child from a binary on
// disk, wait for it to listen, answer. Every request that finds its plugin
// stopped — the first after boot, the first after IdleAfter reaped it — pays
// this, so it is the cold tail of every lazily mounted prefix.
//
// The binary is the test plugin padded with trailing bytes to the sizes a real
// fleet ships (Hanzo's per-app plugins run 24-145 MB). Trailing bytes are never
// mapped, so the process's own startup is the same at every size; what scales
// with the file is whatever the host does with the file before it execs it.
//
//	go test -run='^$' -bench=Benchmark_ColdStart -benchtime=40x -count=6 .

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/zap-proto/zip"
)

func Benchmark_ColdStart(b *testing.B) {
	base := buildPluginBin(b, "v1")
	for _, mb := range []int{0, 32, 128} {
		path := padded(b, base, mb)
		b.Run(fmt.Sprintf("pad=%dMB", mb), func(b *testing.B) {
			dir := sockDir(b)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				host := zip.New(benchConfig())
				host.Use(must(zip.Load(zip.Plugin{Name: "demo", Path: path, Dir: dir, Lazy: true}, "/v1/demo")))
				h := host.Fiber().Handler()
				fctx := getFctx("/v1/demo/version")
				b.StartTimer()
				h(fctx)
				b.StopTimer()
				if code := fctx.Response.StatusCode(); code != 200 {
					b.Fatalf("first request answered %d: %s", code, fctx.Response.Body())
				}
				_ = host.Shutdown()
				b.StartTimer()
			}
		})
	}
}

// padded copies bin to a file mb MiB longer, beside it.
func padded(b *testing.B, bin string, mb int) string {
	b.Helper()
	out := filepath.Join(b.TempDir(), fmt.Sprintf("demo-%d", mb))
	src, err := os.Open(bin)
	if err != nil {
		b.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		b.Fatal(err)
	}
	if _, err := dst.Write(make([]byte, mb<<20)); err != nil {
		b.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		b.Fatal(err)
	}
	return out
}
