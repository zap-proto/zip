package zip_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestBasicRouting hits the hello-world path through fiber.Test to
// confirm the Sinatra idiom + JSON response work end-to-end.
func TestBasicRouting(t *testing.T) {
	app := zip.New(zip.Config{AppName: "test", DisableStartupMessage: true})
	app.Get("/hello", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"message": "hi"})
	})

	req, _ := http.NewRequest("GET", "/hello", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json: %v / body=%s", err, body)
	}
	if got["message"] != "hi" {
		t.Fatalf("body=%s", body)
	}
}

// TestHTTPError checks zip.HTTPError → JSON error response.
func TestHTTPError(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/boom", func(c *zip.Ctx) error {
		return zip.ErrNotFound("nope")
	})
	req, _ := http.NewRequest("GET", "/boom", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "nope") {
		t.Fatalf("body=%s", body)
	}
}

// TestTyped exercises the generic typed handler + reflection-based
// validation + OpenAPI route installation.
func TestTyped(t *testing.T) {
	type In struct {
		Email string `json:"email" validate:"required,minlen=3"`
	}
	type Out struct {
		OK bool `json:"ok"`
	}
	app := zip.New(zip.Config{DisableStartupMessage: true})
	zip.Post(app, "/v1/test", func(ctx context.Context, in *In) (*Out, error) {
		return &Out{OK: true}, nil
	})

	// Valid call.
	req, _ := http.NewRequest("POST", "/v1/test",
		strings.NewReader(`{"email":"z@hanzo.ai"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body=%s", resp.StatusCode, body)
	}

	// Invalid call — missing email.
	req2, _ := http.NewRequest("POST", "/v1/test",
		strings.NewReader(`{}`))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := app.Fiber().Test(req2)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp2.StatusCode != 400 {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("status %d body=%s, want 400", resp2.StatusCode, body)
	}
}

// TestGroup verifies app.Group prefixing.
func TestGroup(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	v1 := app.Group("/v1")
	v1.Get("/ping", func(c *zip.Ctx) error {
		return c.String(200, "pong")
	})
	req, _ := http.NewRequest("GET", "/v1/ping", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// TestIdentityHeaders confirms c.Org/User/Email map to X-* headers.
func TestIdentityHeaders(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/who", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{
			"org":  c.Org(),
			"user": c.User(),
		})
	})
	req, _ := http.NewRequest("GET", "/who", nil)
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Id", "z")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"org":"hanzo"`) ||
		!strings.Contains(string(body), `"user":"z"`) {
		t.Fatalf("body=%s", body)
	}
}
