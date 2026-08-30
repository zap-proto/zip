package zip_test

// A URL carries one value per name, so a REPEATED argument is written as one
// value with commas — `style: form, explode: false`, which is what the document
// publishes for it. Until the binder read that spelling, an op taking a list of
// ids answered 200 about an empty list. These pin the spelling, and pin the
// document to naming exactly what the binder fills.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/zap-proto/zip"
)

type listIn struct {
	IDs   []hash            `json:"ids"`
	Names []string          `json:"names"`
	Sizes []int             `json:"sizes"`
	By    map[string]string `json:"by"`
}

func echoList(_ context.Context, in *listIn) (*listIn, error) { return in, nil }

func listApp() *zip.App {
	a := zip.New(zip.Config{AppName: "list", DisableStartupMessage: true})
	zip.Get(a, "/v1/list/things", echoList)
	return a
}

func askList(t *testing.T, query string) listIn {
	t.Helper()
	code, body := call2(t, listApp(), "GET", "/v1/list/things?"+query, "")
	if code != 200 {
		t.Fatalf("status = %d %s", code, body)
	}
	var got listIn
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return got
}

// A repeated field arrives, and its elements are read the same way a bare one
// would be — so a list of ids says nothing about ids a second time.
func TestAURLCarriesARepeatedValue(t *testing.T) {
	one, two := hash{0x11}, hash{0x22}
	got := askList(t, "ids="+hex.EncodeToString(one[:])+","+hex.EncodeToString(two[:])+
		"&names=a,b&sizes=1,2,3")

	if len(got.IDs) != 2 || got.IDs[0] != one || got.IDs[1] != two {
		t.Errorf("ids = %x, want the two named", got.IDs)
	}
	if len(got.Names) != 2 || got.Names[0] != "a" || got.Names[1] != "b" {
		t.Errorf("names = %v", got.Names)
	}
	if len(got.Sizes) != 3 || got.Sizes[2] != 3 {
		t.Errorf("sizes = %v", got.Sizes)
	}
}

// One element is still a list. A caller asking about a single id sends one
// value, and an op that took a list only when given two would be a trap.
func TestOneValueIsAListOfOne(t *testing.T) {
	if got := askList(t, "names=solo"); len(got.Names) != 1 || got.Names[0] != "solo" {
		t.Errorf("names = %v, want [solo]", got.Names)
	}
}

// A map is not a list with different punctuation. It has no URL spelling, so
// the binder leaves it alone and the document does not name it.
func TestAMapDoesNotRideAURL(t *testing.T) {
	if got := askList(t, "by=k:v"); got.By != nil {
		t.Errorf("by = %v, want nil", got.By)
	}
}

// The other half of the same rule: the document names exactly what the binder
// fills, so a generated client's arguments and the server's binding cannot
// disagree. An id is a STRING there — one word in the URL and the same word in
// the JSON beside it, whatever it is made of — and a list of ids is an array of
// that string. Describing it by what it is MADE of published an array of
// integers for a value nothing spells that way.
func TestTheDocumentNamesWhatTheBinderFills(t *testing.T) {
	type oneIn struct {
		ID hash `json:"id"`
	}
	a := listApp()
	zip.Get(a, "/v1/list/one", func(_ context.Context, in *oneIn) (*oneIn, error) { return in, nil })

	says := func(path string) map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, raw := range paramsOf2(t, a, path, "get") {
			p, _ := raw.(map[string]any)
			schema, _ := p["schema"].(map[string]any)
			kind, _ := schema["type"].(string)
			if kind == "array" {
				items, _ := schema["items"].(map[string]any)
				kind += " of " + items["type"].(string)
			}
			out[p["name"].(string)] = kind
		}
		return out
	}

	list := says("/v1/list/things")
	for name, want := range map[string]string{
		"ids":   "array of string",
		"names": "array of string",
		"sizes": "array of integer",
	} {
		if list[name] != want {
			t.Errorf("%s: document says %q, want %q", name, list[name], want)
		}
	}
	if _, named := list["by"]; named {
		t.Errorf("the document names `by`, which the binder cannot fill")
	}
	if one := says("/v1/list/one"); one["id"] != "string" {
		t.Errorf("id: document says %q, want a string", one["id"])
	}
}
