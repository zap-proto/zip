package zip_test

// A record's leaves ride a URL, named through the record. A chain's UTXO read
// takes a pagination cursor that way — an address and a utxo id inside one
// field — and with no URL spelling for it the read had no safe method it could
// be served on at all.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

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

// The record binds beside the list, because a real cursor arrives with the
// query it continues.
func TestURLCarriesARecordBesideItsList(t *testing.T) {
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
