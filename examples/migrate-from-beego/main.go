// migrate-from-beego example — beego → zip migration via http.Handler
// adapter. beego.BeeApp exposes Handlers as an http.Handler-compatible
// surface; the same AdaptNetHTTP adapter that fronted chi handles beego too.
//
// In a real port:
//
//	import (
//	    "github.com/beego/beego/v2/server/web"
//	    "github.com/zap-proto/zip"
//	)
//
//	beeApp := web.NewHttpSever()  // your existing beego app
//	zipApp := zip.New(zip.Config{AppName: "iam"})
//	zipApp.Group("/legacy/iam").All("/*", zip.AdaptNetHTTP(beeApp.Handlers))
//
// This example uses a stand-in http.Handler so the file builds without
// pulling beego — same adapter pattern in either case.
//
// The adapted subtree is a wildcard route and registers no operation, so nothing
// behind /legacy/iam is in the OpenAPI document, the MCP tool list, the CLI or
// the call plane. New work goes in as a TYPED op, and a native route added later
// wins by specificity — so the legacy tree shrinks one op at a time.
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/zap-proto/zip"
)

type beegoStub struct{}

func (beegoStub) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"legacy_beego":true}`))
}

// Health is what the probe answers.
type Health struct {
	Status string `json:"status"`
}

// Nothing is an operation that takes no input.
type Nothing struct{}

// Healthz reports whether IAM is up.
func healthz(context.Context, *Nothing) (*Health, error) {
	return &Health{Status: "ok"}, nil
}

func main() {
	app := zip.New(zip.Config{AppName: "migrate-from-beego"})

	// New native zip routes, typed:
	zip.Get(app, "/v1/iam/healthz", healthz)

	// Legacy beego app under /legacy/iam — one adapted wildcard route:
	app.Group("/legacy/iam").All("/*", zip.AdaptNetHTTP(beegoStub{}))

	log.Fatal(app.Listen("http://:8080"))
}
