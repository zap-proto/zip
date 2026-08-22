// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// linkIn takes nothing; linkOut is the smallest shape that makes the op typed,
// which is what turns an app's description on.
type linkIn struct{}

type linkOut struct {
	Name string `json:"name"`
}

// linked builds an app that serves a document — one typed op is what turns the
// description on — and returns its response to one GET.
func linked(t *testing.T, path string) *http.Response {
	t.Helper()
	a := quiet("linked")
	Get(a, "/thing", func(ctx context.Context, _ *linkIn) (*linkOut, error) {
		return &linkOut{Name: "thing"}, nil
	})
	a.installOpenAPIRoutes()
	if err := a.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	resp, err := a.Fiber().Test(httptest.NewRequest("GET", path, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// rel returns the target of one link relation, or "" when the answer carries none.
func rel(resp *http.Response, name string) string {
	for _, l := range resp.Header.Values("Link") {
		if !strings.Contains(l, `rel="`+name+`"`) {
			continue
		}
		if i, j := strings.Index(l, "<"), strings.Index(l, ">"); i == 0 && j > 0 {
			return l[1:j]
		}
	}
	return ""
}

// THE PROPERTY: a link never names an address this app does not serve.
//
// It is the whole reason the header is derived from the same map that installs
// the route rather than written beside it. A description advertised at an
// address that 404s is worse than none — a generic client follows it, gets
// nothing, and reports the API as broken rather than as undescribed.
func TestLink_NamesOnlyAddressesThisAppServes(t *testing.T) {
	a := quiet("served")
	Get(a, "/thing", func(ctx context.Context, _ *linkIn) (*linkOut, error) {
		return &linkOut{Name: "thing"}, nil
	})
	a.installOpenAPIRoutes()
	if err := a.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	resp, err := a.Fiber().Test(httptest.NewRequest("GET", "/thing", nil))
	if err != nil {
		t.Fatalf("GET /thing: %v", err)
	}
	targets := map[string]string{
		relDesc: rel(resp, relDesc),
		relDoc:  rel(resp, relDoc),
	}
	for name, at := range targets {
		if at == "" {
			t.Fatalf("no %s link on the answer", name)
		}
		// FOLLOW it, which is the only check that can fail for the right reason.
		got, err := a.Fiber().Test(httptest.NewRequest("GET", at, nil))
		if err != nil {
			t.Fatalf("following %s -> %s: %v", name, at, err)
		}
		_, _ = io.Copy(io.Discard, got.Body)
		_ = got.Body.Close()
		if got.StatusCode != 200 {
			t.Errorf("%s names %s, which answers %d — a link must not advertise an "+
				"address this app does not serve", name, at, got.StatusCode)
		}
	}
}

// self is the address the caller REACHED, which is the one thing a client cannot
// reconstruct: it may have arrived through a proxy, an alias or a redirect.
func TestLink_SelfIsTheAddressReached(t *testing.T) {
	resp := linked(t, "/thing")
	if got := rel(resp, relSelf); got != "/thing" {
		t.Errorf("self = %q, want /thing", got)
	}
}

// AN APP WITH NO DESCRIPTION SAYS NOTHING. The middleware is absent rather than
// present-and-quiet, so an app that disabled its document pays nothing per
// request and advertises nothing that would 404.
func TestLink_AnAppWithNoDocumentAdvertisesNothing(t *testing.T) {
	a := New(Config{AppName: "silent", DisableStartupMessage: true,
		OpenAPI: OpenAPIConfig{Disabled: true}})
	Get(a, "/thing", func(ctx context.Context, _ *linkIn) (*linkOut, error) {
		return &linkOut{Name: "thing"}, nil
	})
	a.installOpenAPIRoutes() // no-op when Disabled
	if err := a.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	resp, err := a.Fiber().Test(httptest.NewRequest("GET", "/thing", nil))
	if err != nil {
		t.Fatalf("GET /thing: %v", err)
	}
	if got := resp.Header.Values("Link"); len(got) != 0 {
		t.Errorf("an app serving no document sent %v", got)
	}
}

// Link is a LIST header (RFC 8288 §3), so a handler's own links SURVIVE. A page
// naming next and prev keeps them and gets the service relations beside them —
// setting rather than adding would silently drop the handler's own paging.
func TestLink_AHandlersOwnLinksSurvive(t *testing.T) {
	a := quiet("paged")
	// BEFORE the route: zip composes middleware into the routes written after it,
	// so one installed below its leaves never wraps them.
	a.Use(H(func(c *Ctx) error {
		c.fc.Response().Header.Add("Link", `</page?after=2>; rel="next"`)
		return c.Continue()
	}))
	Get(a, "/page", func(ctx context.Context, _ *linkIn) (*linkOut, error) {
		return &linkOut{Name: "page"}, nil
	})
	a.installOpenAPIRoutes()
	if err := a.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	resp, err := a.Fiber().Test(httptest.NewRequest("GET", "/page", nil))
	if err != nil {
		t.Fatalf("GET /page: %v", err)
	}
	if got := rel(resp, "next"); got != "/page?after=2" {
		t.Errorf("the handler's own next link = %q — the service relations replaced "+
			"it instead of joining it", got)
	}
	if rel(resp, relDesc) == "" {
		t.Error("the service description link is missing beside the handler's own")
	}
}
