package zip

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestDetachOutlivesTheRequest pins that a detached context answers with the
// caller its request carried after that request is finished and its RequestCtx
// is back in the server's pool, and that it no longer reaches the request.
func TestDetachOutlivesTheRequest(t *testing.T) {
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	type none struct{}
	var (
		mu  sync.Mutex
		det []context.Context
	)
	app.Get("/v1/hook", func(ctx context.Context, _ *none) (*none, error) {
		mu.Lock()
		det = append(det, Detach(ctx))
		mu.Unlock()
		return &none{}, nil
	}, WithOperationID("hook"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, org := range []string{"acme", "globex", "initech"} {
		req := httptest.NewRequest("GET", "/v1/hook", nil)
		req.Header.Set(HeaderOrg, org)
		req.Header.Set(HeaderUser, "u-"+org)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", org, err)
		}
		_ = resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	for i, org := range []string{"acme", "globex", "initech"} {
		ctx := det[i]
		if requestOf(ctx) != nil {
			t.Fatalf("%s: a detached context still reaches its request", org)
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("%s: a detached context ended with its request: %v", org, err)
		}
		if c := CallerOf(ctx); c.Org != org || c.User != "u-"+org {
			t.Fatalf("%s: detached caller is %+v", org, c)
		}
	}
}
