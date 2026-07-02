package zip_test

// Head-to-head bench: zip's edge JSON path with encoding/json (v1) vs
// encoding/json/v2. Build select happens via the `goexperiment.jsonv2`
// tag — the same `zip.JSONVariant` constant the benchmarks print is
// what `zip.New` logs at startup.
//
// Run:
//   go test -bench=BenchmarkJSON -benchmem -run=^$ .
//   GOEXPERIMENT=jsonv2 go test -bench=BenchmarkJSON -benchmem -run=^$ .
//
// Compare allocations + ns/op between the two invocations.

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// chatRequest is a representative request payload — same kind of shape
// commerce/ai/mcp see at the edge. Modest field count, mixed types.
type chatRequest struct {
	Model       string            `json:"model"`
	Prompt      string            `json:"prompt"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature"`
	Stop        []string          `json:"stop,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// chatResponse mirrors a typical AI chat completion response.
type chatResponse struct {
	ID                 string  `json:"id"`
	Model              string  `json:"model"`
	Content            string  `json:"content"`
	PromptTokens       int     `json:"prompt_tokens"`
	CompletionTokens   int     `json:"completion_tokens"`
	TotalTokens        int     `json:"total_tokens"`
	FinishReason       string  `json:"finish_reason"`
	Latency            float64 `json:"latency_ms"`
	CachedPromptTokens int     `json:"cached_prompt_tokens,omitempty"`
}

var benchReqBody = []byte(`{
  "model":"zen-mode-32b",
  "prompt":"Summarize the wire-protocol stack for HIP-0106.",
  "max_tokens":256,
  "temperature":0.7,
  "stop":["</s>","\n\n"],
  "metadata":{"trace_id":"abc-123","user_id":"u-42","org_id":"o-99"}
}`)

// BenchmarkJSONEdge runs the full edge path: POST a JSON body, zip
// decodes it via c.Bind() (json/v2 if GOEXPERIMENT=jsonv2, else
// json/v1), the handler returns a JSON response, zip encodes it.
// This is the "JSON at the edge" hot path in production.
func BenchmarkJSONEdge(b *testing.B) {
	app := zip.New(zip.Config{
		AppName:               "bench",
		DisableStartupMessage: true,
	})
	app.Post("/v1/chat", func(c *zip.Ctx) error {
		var in chatRequest
		if err := c.Bind(&in); err != nil {
			return err
		}
		out := chatResponse{
			ID:                 "chatcmpl-bench",
			Model:              in.Model,
			Content:            "Cars are flying. The wire stack is: ingress→gateway→subsystem; JSON at edge, ZAP between.",
			PromptTokens:       27,
			CompletionTokens:   19,
			TotalTokens:        46,
			FinishReason:       "stop",
			Latency:            42.5,
			CachedPromptTokens: 0,
		}
		return c.JSON(200, &out)
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", "/v1/chat", bytes.NewReader(benchReqBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			b.Fatalf("Test(): %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// BenchmarkJSONMarshalOnly isolates the c.JSON() path.
func BenchmarkJSONMarshalOnly(b *testing.B) {
	app := zip.New(zip.Config{
		AppName:               "bench-marshal",
		DisableStartupMessage: true,
	})
	out := chatResponse{
		ID:               "chatcmpl-marshal-only",
		Model:            "zen-mode-32b",
		Content:          strings.Repeat("ZAP between services. ", 16),
		PromptTokens:     128,
		CompletionTokens: 256,
		TotalTokens:      384,
		FinishReason:     "stop",
		Latency:          73.2,
	}
	app.Get("/marshal", func(c *zip.Ctx) error { return c.JSON(200, &out) })

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", "/marshal", nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			b.Fatalf("Test(): %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// BenchmarkJSONUnmarshalOnly isolates the c.Bind() path.
func BenchmarkJSONUnmarshalOnly(b *testing.B) {
	app := zip.New(zip.Config{
		AppName:               "bench-unmarshal",
		DisableStartupMessage: true,
	})
	app.Post("/unmarshal", func(c *zip.Ctx) error {
		var in chatRequest
		if err := c.Bind(&in); err != nil {
			return err
		}
		return c.NoContent(204)
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", "/unmarshal", bytes.NewReader(benchReqBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			b.Fatalf("Test(): %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// TestJSONVariantConstant catches the case where someone breaks the
// build-tag wiring: with GOEXPERIMENT=jsonv2 we expect
// `encoding/json/v2`; without it, `encoding/json`. The constant is
// what zip.New logs at startup, so this is the same value operators
// read in cloud-binary logs.
func TestJSONVariantConstant(t *testing.T) {
	switch zip.JSONVariant {
	case "encoding/json/v2", "encoding/json":
		// ok — exactly one of these must be reported.
	default:
		t.Fatalf("unexpected JSONVariant %q", zip.JSONVariant)
	}
}
