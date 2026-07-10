package zaprpc

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/fiber/v3"
)

// echoService is a tiny ZAP service: method "ping" returns its payload
// prefixed with "pong:".
type echoService struct{}

func (echoService) Name() string { return "echo.v1" }
func (echoService) Handle(_ context.Context, method string, payload []byte) ([]byte, error) {
	if method != "ping" {
		return nil, ErrNoService
	}
	return append([]byte("pong:"), payload...), nil
}

func TestEnvelopeRoundTrip(t *testing.T) {
	in := Envelope{Service: "echo.v1", Method: "ping", Payload: []byte("hello")}
	frame := EncodeEnvelope(in)
	out, err := DecodeEnvelope(frame)
	if err != nil {
		t.Fatal(err)
	}
	if out.Service != in.Service || out.Method != in.Method || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("roundtrip mismatch: %+v != %+v", out, in)
	}
}

func TestHTTPHandler(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoService{})

	app := fiber.New()
	app.Post("/zap", HTTPHandler(reg))

	frame := EncodeEnvelope(Envelope{Service: "echo.v1", Method: "ping", Payload: []byte("xyz")})
	req := httptest.NewRequest("POST", "/zap", bytes.NewReader(frame))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zap" {
		t.Fatalf("content-type = %q, want application/zap", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	payload, err := DecodeResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "pong:xyz" {
		t.Fatalf("payload = %q, want pong:xyz", payload)
	}
}

func TestHTTPHandler_UnknownService(t *testing.T) {
	reg := NewRegistry()
	app := fiber.New()
	app.Post("/zap", HTTPHandler(reg))

	frame := EncodeEnvelope(Envelope{Service: "nope.v1", Method: "ping", Payload: []byte("x")})
	resp, err := app.Test(httptest.NewRequest("POST", "/zap", bytes.NewReader(frame)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHTTPHandler_EmptyBody(t *testing.T) {
	reg := NewRegistry()
	app := fiber.New()
	app.Post("/zap", HTTPHandler(reg))
	resp, err := app.Test(httptest.NewRequest("POST", "/zap", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
