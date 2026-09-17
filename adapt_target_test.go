package zip_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// AN ADAPTED HANDLER IS ASKED FOR THE ADDRESS THE CALLER NAMED, and the
// request-target is where a net/http handler reads it.
//
// It is asserted as RequestURI and not as URL.Path, because http.ServeMux
// matches on URL.Path — so a mux-based test is green whatever the target says,
// and the property needs a test that reads the field itself.
//
// The handlers that read the target are the ones that RELAY: a proxy, an ingest
// endpoint, anything that forwards what it was asked for. An empty target
// normalises to "/", so a relay asks for a path nobody named and answers 404 to
// every valid call. net/http's own server always sets this field, which is the
// contract an adapted handler is written against.
func TestAdaptNetHTTP_CarriesTheRequestTarget(t *testing.T) {
	app := zip.New(zip.Config{AppName: "target", DisableStartupMessage: true})

	var target, rawQuery, path string
	app.Group("/relay").Raw(zip.MethodAll, "/*", zip.AdaptNetHTTP(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target, rawQuery, path = r.RequestURI, r.URL.RawQuery, r.URL.Path
			fmt.Fprint(w, "ok")
		})))

	req := httptest.NewRequest("GET", "/relay/logs/livetail?follow=1", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if want := "/relay/logs/livetail?follow=1"; target != want {
		t.Errorf("RequestURI = %q, want %q — a handler that routes on the target is handed nothing", target, want)
	}
	if want := "/relay/logs/livetail"; path != want {
		t.Errorf("URL.Path = %q, want %q", path, want)
	}
	if want := "follow=1"; rawQuery != want {
		t.Errorf("URL.RawQuery = %q, want %q", rawQuery, want)
	}
}
