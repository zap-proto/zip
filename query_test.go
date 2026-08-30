package zip_test

// [zip.Query] and the URL binder are two halves of one rule, and a test that
// only checked one half is how they drifted: the client wrote `?ids=["a","b"]`
// and the binder read a list of one nonsense element, so the op answered 200
// about an argument nobody sent. These hold the halves together — what Query
// writes is what the binder reads, checked by sending it.

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

type cursor struct {
	Address string `json:"address"`
	UTXO    string `json:"utxo"`
}

type pageIn struct {
	IDs   []hash   `json:"ids"`
	Names []string `json:"names"`
	Limit uint32   `json:"limit"`
	Start cursor   `json:"start"`
}

// TestQueryWritesWhatTheBinderReads is a ROUND TRIP, not two assertions about a
// string: the In goes out as a query and comes back off a live op, so a change
// to either half that breaks the other fails here.
func TestQueryWritesWhatTheBinderReads(t *testing.T) {
	one, two := hash{0x11}, hash{0x22}
	sent := &pageIn{
		IDs:   []hash{one, two},
		Names: []string{"a", "b"},
		Limit: 25,
		Start: cursor{Address: "P-lux1abc", UTXO: "2Qouv"},
	}

	query, err := zip.Query(sent)
	if err != nil {
		t.Fatal(err)
	}

	a := zip.New(zip.Config{AppName: "page", DisableStartupMessage: true})
	var got *pageIn
	zip.Get(a, "/v1/page/things", func(_ context.Context, in *pageIn) (*pageIn, error) {
		got = in
		return in, nil
	})
	if _, err := a.Test(httptest.NewRequest("GET", "/v1/page/things?"+query, nil)); err != nil {
		t.Fatal(err)
	}

	if got == nil {
		t.Fatal("the op did not run")
	}
	if len(got.IDs) != 2 || got.IDs[0] != one || got.IDs[1] != two {
		t.Errorf("ids: got %x, want the two sent (query was %q)", got.IDs, query)
	}
	if len(got.Names) != 2 || got.Names[1] != "b" {
		t.Errorf("names: got %v", got.Names)
	}
	if got.Limit != 25 {
		t.Errorf("limit: got %d", got.Limit)
	}
	if got.Start != sent.Start {
		t.Errorf("start: got %+v, want %+v", got.Start, sent.Start)
	}
}

// An absent value is written by leaving the key out, not by sending "null" —
// which the binder would read as the four characters.
func TestQueryLeavesOutWhatIsAbsent(t *testing.T) {
	type optional struct {
		Name  *string `json:"name"`
		Other string  `json:"other"`
	}
	got, err := zip.Query(&optional{Other: "here"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "other=here" {
		t.Errorf("got %q, want just the value that was set", got)
	}
}

// A hash is one word in the query, because that is its written form — the same
// word the binder reads back.
func TestQueryWritesAValueByItsWrittenForm(t *testing.T) {
	type one struct {
		ID hash `json:"id"`
	}
	var h hash
	h[0] = 0xab
	got, err := zip.Query(&one{ID: h})
	if err != nil {
		t.Fatal(err)
	}
	if want := "id=" + hex.EncodeToString(h[:]); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
