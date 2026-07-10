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
//	zipApp.All("/legacy/iam/*", zip.AdaptNetHTTP(beeApp.Handlers))
//
// This example uses a stand-in http.Handler so the file builds without
// pulling beego — same adapter pattern in either case.
package main

import (
	"log"
	"net/http"

	"github.com/zap-proto/zip"
)

type beegoStub struct{}

func (beegoStub) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"legacy_beego":true}`))
}

func main() {
	app := zip.New(zip.Config{AppName: "migrate-from-beego"})

	// New native zip routes:
	app.Get("/v1/iam/healthz", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})

	// Legacy beego app under /legacy/iam — one adapted wildcard route:
	app.All("/legacy/iam/*", zip.AdaptNetHTTP(beegoStub{}))

	log.Fatal(app.Listen("http://:8080"))
}
