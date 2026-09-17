package zip_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

type traceRef struct {
	TraceID string `json:"traceId"`
}

type echoAddr struct {
	Address string `json:"address"`
}

type tracesAPI struct{}

// Spans returns one trace's spans.
func (tracesAPI) Spans(ctx context.Context, _ *traceRef) (*echoAddr, error) {
	a, _ := zip.AddressOf(ctx)
	return &echoAddr{Address: a}, nil
}

// A PARAMETER'S VALUE IS ONE SEGMENT, whatever is in it.
//
// The address is a URL path, so a value substituted into it has to be escaped
// as one: a trace id of "t1/2" is a single segment, not two. Substituting it
// raw produced ".../traces/t1/2", and a relay that forwards what [zip.AddressOf]
// answers then asked its runtime for a route nobody registered — the request
// arrived correctly and the forward went somewhere else.
func TestAddress_AParameterValueIsOneSegment(t *testing.T) {
	app := zip.New(zip.Config{AppName: "traces", DisableStartupMessage: true})
	app.Group("/v1/o11y").Get("/traces/:traceId", tracesAPI{}.Spans)

	for _, c := range []struct{ raw, want string }{
		{"t1%2F2", "/v1/o11y/traces/t1%2F2"}, // a slash must not split the path
		{"t1%202", "/v1/o11y/traces/t1%202"}, // nor a space widen it
		{"plain", "/v1/o11y/traces/plain"},   // and the ordinary case is untouched
	} {
		resp, err := app.Test(httptest.NewRequest("GET", "/v1/o11y/traces/"+c.raw, nil))
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		var got echoAddr
		_ = json.NewDecoder(resp.Body).Decode(&got)
		_ = resp.Body.Close()
		if got.Address != c.want {
			t.Errorf("requesting %q: address = %q, want %q", c.raw, got.Address, c.want)
		}
	}
}

// And the same through the direct helper, which is what a service that builds
// its own address calls.
func TestAddress_EscapesEachValue(t *testing.T) {
	got := zip.Address("/v1/traces/:traceId", &traceRef{TraceID: "t1/2"})
	if want := "/v1/traces/t1%2F2"; got != want {
		t.Errorf("Address = %q, want %q", got, want)
	}
}
