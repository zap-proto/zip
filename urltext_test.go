package zip_test

// A URL carries characters, and some values are written as characters without
// being a Go string. An id is the case that forced this: a 32-byte array is not
// a kind the binder could convert into, so every route addressing a resource by
// id published a parameter that bound nothing and ran the handler on a zero id —
// a wrong answer with a 200 on it. The type already says how to read itself from
// text; these pin that the binder asks.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// hash is the shape of every id in the fleet: an array, written as text.
type hash [4]byte

func (h hash) MarshalText() ([]byte, error) { return []byte(hex.EncodeToString(h[:])), nil }

func (h *hash) UnmarshalText(text []byte) error {
	raw, err := hex.DecodeString(string(text))
	if err != nil {
		return err
	}
	if len(raw) != len(h) {
		return errors.New("hash is 4 bytes")
	}
	copy(h[:], raw)
	return nil
}

// tone is a named string that ALSO reads text, and reads it differently. The
// type decides, so it gets the reading it declared.
type tone string

func (t *tone) UnmarshalText([]byte) error { *t = "read-as-text"; return nil }

type textIn struct {
	ID    hash  `json:"id"`
	Prior *hash `json:"prior"`
	Tone  tone  `json:"tone"`
}

type textOut struct {
	ID    hash  `json:"id"`
	Prior *hash `json:"prior"`
	Tone  tone  `json:"tone"`
}

func echoText(_ context.Context, in *textIn) (*textOut, error) {
	return &textOut{ID: in.ID, Prior: in.Prior, Tone: in.Tone}, nil
}

func rawText(t *testing.T, path string) string {
	t.Helper()
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	zip.Get(a, "/v1/t/thing", echoText)
	zip.Get(a, "/v1/t/thing/:id", echoText)

	res, err := a.Fiber().Test(httpGet(t, path))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, res.StatusCode, body)
	}
	return string(body)
}

func askText(t *testing.T, path string) textOut {
	t.Helper()
	body := rawText(t, path)
	var out textOut
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("GET %s: %v over %s", path, err, body)
	}
	return out
}

func httpGet(t *testing.T, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestURLBindsAValueThatReadsItselfFromText(t *testing.T) {
	got := askText(t, "/v1/t/thing?id=deadbeef&prior=0badcafe")
	if want := (hash{0xde, 0xad, 0xbe, 0xef}); got.ID != want {
		t.Errorf("query id: got %x, want %x", got.ID, want)
	}
	if got.Prior == nil {
		t.Fatal("query prior: nil, want a value")
	}
	if want := (hash{0x0b, 0xad, 0xca, 0xfe}); *got.Prior != want {
		t.Errorf("query prior: got %x, want %x", *got.Prior, want)
	}

	if got := askText(t, "/v1/t/thing/deadbeef"); got.ID != (hash{0xde, 0xad, 0xbe, 0xef}) {
		t.Errorf("path id: got %x, want deadbeef", got.ID)
	}
}

func TestURLLeavesTheZeroWhenTextCannotBeRead(t *testing.T) {
	got := askText(t, "/v1/t/thing?id=notahash")
	if got.ID != (hash{}) {
		t.Errorf("got %x, want the zero id: a value that cannot be read is not half-read", got.ID)
	}
	if got.Prior != nil {
		t.Errorf("got %v, want nil: an absent value is not an allocated one", got.Prior)
	}
}

// A type that declares how to read itself from text gets that reading, even
// where its KIND could have carried the string on its own.
//
// This is encoding/json's own precedence — a named string with an UnmarshalText
// is decoded through it, not by assignment — so a URL and a JSON body on the
// same struct read one word one way. Deciding by kind first also put the rule
// out of reach of the values that need it most: a written form is usually
// carried by a NUMERIC kind, and a height spelled "proposed" or an encoding
// spelled "json" met ParseUint, failed, and bound zero.
//
// The answer is read as raw text, not unmarshalled: tone reads text on the way
// IN too, so decoding the reply would apply the very rule under test to it.
func TestTheTypeDecidesBeforeItsKind(t *testing.T) {
	got := rawText(t, "/v1/t/thing?tone=plain")
	if !strings.Contains(got, `"tone":"read-as-text"`) {
		t.Errorf("got %s, want the reading tone declared", got)
	}
}

// count is the case that forced the ordering: a written form carried by a
// numeric kind, whose word is not a number.
type count uint64

func (c *count) UnmarshalText(text []byte) error {
	if string(text) == "all" {
		*c = 1 << 20
		return nil
	}
	n, err := strconv.ParseUint(string(text), 10, 64)
	*c = count(n)
	return err
}

func TestAWrittenFormOnANumericKindIsRead(t *testing.T) {
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	type countIn struct {
		N count `json:"n"`
	}
	zip.Get(a, "/v1/t/count", func(_ context.Context, in *countIn) (*countIn, error) { return in, nil })

	for word, want := range map[string]count{"all": 1 << 20, "7": 7} {
		resp, err := a.Test(httptest.NewRequest("GET", "/v1/t/count?n="+word, nil))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		// Read as a plain number: count reads TEXT on the way in, so decoding
		// the reply into it would apply the rule under test to the answer.
		var got struct {
			N uint64 `json:"n"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.N != uint64(want) {
			t.Errorf("n=%s bound %d, want %d", word, got.N, want)
		}
	}
}
