package zip

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Benchmark_OpenBinary prices what a start does with the plugin's file before
// it execs it, at the sizes a fleet ships (Hanzo's per-app plugins: 24-145 MB),
// for a plugin with no declared Sum — the shape every on-disk plugin takes.
//
//	go test -run='^$' -bench=Benchmark_OpenBinary -count=6 .
func Benchmark_OpenBinary(b *testing.B) {
	for _, mb := range []int{16, 48, 144} {
		src := filepath.Join(b.TempDir(), "svc")
		if err := os.WriteFile(src, make([]byte, mb<<20), 0o700); err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("size=%dMB", mb), func(b *testing.B) {
			b.SetBytes(int64(mb) << 20)
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				priv, err := os.MkdirTemp(filepath.Dir(src), "zip-")
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				bin, err := openVerified(src, priv, "svc", "")
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				_ = bin.Close()
				_ = os.RemoveAll(priv)
				b.StartTimer()
			}
		})
	}
}
