package zip

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// One artifact serving many plugins must be ONE download and one cached file:
// a multi-call binary mounted under 108 names is 195MB, not 108 x 195MB.
func TestFetch_SharedArtifactCachedOnceAcrossNames(t *testing.T) {
	bin := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(bin)
	digest := hex.EncodeToString(sum[:])

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Write(bin)
	}))
	defer srv.Close()

	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"dns", "flags", "iam", "billing"} {
		p, err := fetch(Plugin{Name: name, URL: srv.URL, Sum: digest, Dir: dir})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		paths = append(paths, p)
	}
	if hits != 1 {
		t.Fatalf("downloaded %d times, want 1 — the digest is the cache key", hits)
	}
	for _, p := range paths[1:] {
		if p != paths[0] {
			t.Fatalf("same digest cached at two paths: %s vs %s", paths[0], p)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "zip-plugins")); len(ents) != 1 {
		t.Fatalf("%d cache entries for one artifact, want 1", len(ents))
	}
}

// Different bytes must never collide, whatever they are called.
func TestFetch_DifferentDigestsAreDistinct(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, body := range []string{"#!/bin/sh\nexit 0\n", "#!/bin/sh\nexit 1\n"} {
		b := []byte(body)
		sum := sha256.Sum256(b)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write(b)
		}))
		p, err := fetch(Plugin{Name: "same", URL: srv.URL, Sum: hex.EncodeToString(sum[:]), Dir: dir})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if paths[0] == paths[1] {
		t.Fatal("two different binaries cached at one path")
	}
}
