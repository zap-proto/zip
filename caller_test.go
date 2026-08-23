package zip

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// A service that must reach another on this caller's behalf needs the credential
// the caller presented. CallerOf deliberately does not carry it, so this is the
// one way to read it — and it must answer honestly when there is no request.
func TestHeaderReadsTheRequestAndSaysNothingWhenThereIsNone(t *testing.T) {
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	type none struct{}
	type out struct {
		Auth string `json:"auth"`
	}
	Get(app, "/v1/echo", func(ctx context.Context, _ *none) (*out, error) {
		return &out{Auth: Header(ctx, "Authorization")}, nil
	}, WithOperationID("echo"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	req := httptest.NewRequest("GET", "/v1/echo", nil)
	req.Header.Set("Authorization", "Bearer abc")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Bearer abc") {
		t.Fatalf("the request header did not reach the handler: %s", body)
	}
	if got := Header(context.Background(), "Authorization"); got != "" {
		t.Errorf("a context with no request behind it answered %q, want empty", got)
	}
}
