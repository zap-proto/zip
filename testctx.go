package zip

import "github.com/valyala/fasthttp"

// TestCtx returns a detached *Ctx over a synthetic request — the unit-test
// analog of a live request context, for calling a Handler directly. Integration
// tests should prefer app.Fiber().Test(req), which exercises routing and the
// full middleware chain; this exists for the narrower "call this one handler
// with locals seeded" idiom. The Ctx is not pooled; do not release it.
func (a *App) TestCtx(method, path string) *Ctx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.URI().SetPath(path)
	return &Ctx{fc: a.fiber.AcquireCtx(fctx), app: a, log: a.logger}
}
