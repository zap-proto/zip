package zip_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/internal/codec"
)

// TestTheCodecIsCheckedIn regenerates internal/codec/zap_gen.go and requires the
// file on disk to match. The generated codec is the wire those types speak, so a
// change to the types that nobody regenerated is a wire that moved under its
// readers — and this is where that is noticed, not in a rolling deploy.
func TestTheCodecIsCheckedIn(t *testing.T) {
	got, err := zip.Codecs(reflect.TypeOf(codec.Trunk{}), reflect.TypeOf(codec.Ided{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d packages, want 1", len(got))
	}
	path := filepath.Join("internal", "codec", "zap_gen.go")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[0].Source) != string(want) {
		if os.Getenv("WRITE") != "" {
			if err := os.WriteFile(path, got[0].Source, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Fatal("wrote " + path + "; run again")
		}
		t.Errorf("%s is stale; regenerate with WRITE=1 go test -run TestTheCodecIsCheckedIn ./...", path)
	}
}

// TestTheEmittedSourceCompiles builds the package the emitter wrote. format.Source
// only proves it PARSES; a codec that names a field that moved, or converts a
// value the type will not take, parses and does not build.
func TestTheEmittedSourceCompiles(t *testing.T) {
	out, err := exec.Command("go", "build", "./internal/codec").CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./internal/codec: %v\n%s", err, out)
	}
}
