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
// kind decides first, so it keeps the reading it has always had.
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

// The answer is read as raw text, not unmarshalled: tone reads text on the way
// IN too, so decoding the reply would apply the very rule under test to it.
func TestTheKindDecidesBeforeTheTextForm(t *testing.T) {
	got := rawText(t, "/v1/t/thing?tone=plain")
	if !strings.Contains(got, `"tone":"plain"`) {
		t.Errorf("got %s, want tone plain: a string binds as a string", got)
	}
}

// A URL carries a list the way it always has, comma-separated, and names a
// record's leaves through the record. Both are what the UTXO reads of a chain
// need — several addresses and a pagination cursor — and neither had a URL
// spelling before, so those reads had no safe method they could be served on.

type page struct {
	Address string `json:"address"`
	UTXO    string `json:"utxo"`
}

type fetchIn struct {
	Addresses []string `json:"addresses"`
	Limit     uint32   `json:"limit"`
	Start     page     `json:"start"`
	Resume    *page    `json:"resume"`
}

func fetch(_ context.Context, in *fetchIn) (*fetchIn, error) { return in, nil }

func askFetch(t *testing.T, path string) fetchIn {
	t.Helper()
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	zip.Get(a, "/v1/t/utxos", fetch)
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
	var out fetchIn
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("GET %s: %v over %s", path, err, body)
	}
	return out
}

func TestURLCarriesAListAndARecord(t *testing.T) {
	got := askFetch(t, "/v1/t/utxos?addresses=X-one,X-two&limit=7&start.address=X-one&start.utxo=abc")
	if len(got.Addresses) != 2 || got.Addresses[0] != "X-one" || got.Addresses[1] != "X-two" {
		t.Errorf("addresses: got %q, want [X-one X-two]", got.Addresses)
	}
	if got.Limit != 7 {
		t.Errorf("limit: got %d, want 7", got.Limit)
	}
	if got.Start != (page{Address: "X-one", UTXO: "abc"}) {
		t.Errorf("start: got %+v, want the cursor the caller wrote", got.Start)
	}
	if got.Resume != nil {
		t.Errorf("resume: got %+v, want nil: a record nobody named is absent, not empty", got.Resume)
	}
}

func TestABareNameNeverReachesInsideARecord(t *testing.T) {
	got := askFetch(t, "/v1/t/utxos?address=X-sneak")
	if got.Start.Address != "" {
		t.Errorf("start.address: got %q, want empty: a leaf is named through its record", got.Start.Address)
	}
}

func TestAPointerRecordIsAllocatedOnlyWhenNamed(t *testing.T) {
	got := askFetch(t, "/v1/t/utxos?resume.utxo=def")
	if got.Resume == nil {
		t.Fatal("resume: nil, want the record the caller named")
	}
	if got.Resume.UTXO != "def" {
		t.Errorf("resume.utxo: got %q, want def", got.Resume.UTXO)
	}
}

// The document publishes exactly what the binder fills — one predicate, two
// readers — so a caller reading the spec writes names that work.
func TestTheDocumentPublishesEveryNameTheURLCanCarry(t *testing.T) {
	a := zip.New(zip.Config{AppName: "t", DisableStartupMessage: true})
	zip.Get(a, "/v1/t/utxos", fetch)
	spec, err := json.Marshal(a.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`"addresses"`, `"limit"`, `"start.address"`, `"start.utxo"`, `"resume.address"`, `"resume.utxo"`} {
		if !strings.Contains(string(spec), name) {
			t.Errorf("the document does not publish the parameter %s the binder fills", name)
		}
	}
}
