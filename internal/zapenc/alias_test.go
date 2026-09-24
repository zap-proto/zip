package zapenc_test

import (
	"testing"

	"github.com/zap-proto/zip/internal/zapenc"
)

// A decoded value owns its strings. The transport hands Unmarshal a pooled frame
// and reuses it for the next call, so a string that still pointed into the frame
// would change under whoever kept it.
func TestDecodedTextOutlivesTheFrame(t *testing.T) {
	type site struct {
		Bucket string
		Prefix string
		Opt    *string
		Inner  inner
	}
	s := "gone"
	frame, err := zapenc.Marshal(&site{Bucket: "hanzo-sites", Prefix: "hanzo/bootnode", Opt: &s, Inner: inner{Slug: "bootnode"}})
	if err != nil {
		t.Fatal(err)
	}
	var got site
	if err := zapenc.Unmarshal(frame, &got); err != nil {
		t.Fatal(err)
	}
	for i := range frame {
		frame[i] = 'x'
	}
	if got.Bucket != "hanzo-sites" || got.Prefix != "hanzo/bootnode" || got.Opt == nil || *got.Opt != "gone" || got.Inner.Slug != "bootnode" {
		t.Fatalf("decoded strings follow the frame: %+v opt=%v", got, got.Opt)
	}
}
