package zip

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// dialRecorder is a Client that records what it was handed and answers 200. It
// stands in for a plugin child, which is what makes the assertion meaningful:
// the child's view of the request is the whole question here.
// It records the host the child would actually READ, which is the host on the
// SERIALIZED request — not a field of the in-memory object. fasthttp writes the
// Host header from the URI whenever the URI has been parsed, so reading either
// field directly can disagree with what goes over the wire. Serializing is the
// only reading that cannot.
type dialRecorder struct{ sawHost string }

func (d *dialRecorder) Do(req *fasthttp.Request, resp *fasthttp.Response) error {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	_ = req.Write(w)
	_ = w.Flush()
	for _, line := range strings.Split(buf.String(), "\r\n") {
		if v, ok := strings.CutPrefix(line, "Host: "); ok {
			d.sawHost = v
			break
		}
	}
	resp.SetStatusCode(200)
	return nil
}

// A PROXIED REQUEST REACHES THE CHILD AS THE HOST IT WAS ASKED AS.
//
// The child dials its own address, so this header is not routing — it is the
// only way a child can know which brand was asked, and a multi-brand subsystem
// cannot answer without it. hanzoai/iam resolves each brand's OIDC issuer from
// exactly this value: overwritten with the socket path, every brand collapsed
// onto one issuer and every non-default brand's tokens were rejected by its own
// relying parties.
func TestForwardKeepsTheCallersHost(t *testing.T) {
	rec := &dialRecorder{}
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetHost("lux.id")
	req.Header.SetRequestURI("/.well-known/openid-configuration")

	if err := forward(req, resp, rec, "/var/run/iam.sock", "", "mount /v1/iam"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if rec.sawHost != "lux.id" {
		t.Fatalf("child saw Host %q, want %q — a brand-resolving child cannot answer as the brand it was asked as", rec.sawHost, "lux.id")
	}
}

// A request with no Host of its own still gets one, because HTTP/1.1 requires
// the header. That is the MCP hop: a freshly built request rather than a
// proxied inbound, and the dial address is the honest answer there.
func TestForwardFallsBackToTheDialAddress(t *testing.T) {
	rec := &dialRecorder{}
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetRequestURI("/mcp")

	if err := forward(req, resp, rec, "plugin.sock", "", "mcp iam"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if rec.sawHost != "plugin.sock" {
		t.Fatalf("a hostless request reached the child as %q, want the dial address", rec.sawHost)
	}
}

// The path rewrite is independent of the host, and both still apply together.
func TestForwardStillRewritesThePath(t *testing.T) {
	rec := &dialRecorder{}
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetHost("hanzo.ai")
	req.Header.SetRequestURI("/v1/iam/mcp")

	if err := forward(req, resp, rec, "iam.sock", "/mcp", "mcp iam"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if got := string(req.URI().Path()); got != "/mcp" {
		t.Fatalf("path = %q, want /mcp", got)
	}
	if rec.sawHost != "hanzo.ai" {
		t.Fatalf("host = %q, want hanzo.ai", rec.sawHost)
	}
}

// A dead target is 503 and names itself, unchanged by any of the above.
func TestForwardWithNoClientIs503(t *testing.T) {
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	err := forward(req, resp, nil, "iam.sock", "", "mount /v1/iam")
	if err == nil || !strings.Contains(err.Error(), "no instance running") {
		t.Fatalf("err = %v, want a 503 naming the mount", err)
	}
}
