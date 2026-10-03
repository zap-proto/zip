package zip

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestTagWriterKeepsLinesWhole pins that lines from several children reach the
// shared stream whole and under their own tag, however each child splits its
// writes, and that a last line without a newline survives Flush.
func TestTagWriterKeepsLinesWhole(t *testing.T) {
	var out bytes.Buffer
	names := []string{"ai", "kms", "flag", "admin"}
	ws := make([]*tagWriter, len(names))
	for i, n := range names {
		ws[i] = &tagWriter{w: &out, tag: []byte("[" + n + "] ")}
	}
	const lines = 500
	var wg sync.WaitGroup
	for i, w := range ws {
		wg.Add(1)
		go func(name string, w *tagWriter) {
			defer wg.Done()
			for j := range lines {
				line := fmt.Sprintf(`{"from":%q,"n":%d}`+"\n", name, j)
				// Split each line across three writes, the way a child's
				// buffered logger can.
				third := len(line) / 3
				for _, part := range []string{line[:third], line[third : 2*third], line[2*third:]} {
					if _, err := w.Write([]byte(part)); err != nil {
						t.Error(err)
						return
					}
				}
			}
			if _, err := w.Write([]byte(`{"from":"` + name + `","last":true}`)); err != nil {
				t.Error(err)
			}
		}(names[i], w)
	}
	wg.Wait()
	for _, w := range ws {
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if want := len(names) * (lines + 1); len(got) != want {
		t.Fatalf("%d lines, want %d", len(got), want)
	}
	for _, l := range got {
		tag, rest, ok := strings.Cut(l, "] ")
		if !ok || !strings.HasPrefix(tag, "[") {
			t.Fatalf("untagged line %q", l)
		}
		if !strings.HasPrefix(rest, `{"from":"`+tag[1:]+`"`) || strings.Contains(rest, "] ") {
			t.Fatalf("line under the wrong tag: %q", l)
		}
	}
}
