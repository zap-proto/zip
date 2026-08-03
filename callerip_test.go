package zip

import (
	"context"
	"net/http/httptest"
	"testing"
)

type ipIn struct{}
type ipOut struct {
	IP string `json:"ip"`
}

func ipApp(t *testing.T, cfg Config) *App {
	t.Helper()
	cfg.DisableStartupMessage = true
	a := New(cfg)
	Get(a, "/whoami", func(ctx context.Context, _ *ipIn) (*ipOut, error) {
		return &ipOut{IP: CallerOf(ctx).IP}, nil
	}, WithOperationID("whoami"))
	return a
}

func whoami(t *testing.T, a *App, xff string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/whoami", nil)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := a.Test(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return readAll(t, resp.Body)
}

// By default an untrusted X-Forwarded-For is IGNORED. Anyone can send one, so
// believing it would make an audited or rate-limited value attacker-controlled.
func TestCallerIP_ForwardedHeaderIsNotBelievedByDefault(t *testing.T) {
	a := ipApp(t, Config{AppName: "svc"})
	got := whoami(t, a, "203.0.113.9")
	if got == `{"ip":"203.0.113.9"}` {
		t.Error("an unauthenticated X-Forwarded-For was believed — that value is attacker-controlled")
	}
	if got == `{"ip":""}` {
		t.Error("no IP at all; the socket peer should still be reported")
	}
}

// Opting in is allowlist-gated: the forwarded value is honoured only when the
// PEER is a named proxy.
func TestCallerIP_TrustedProxyIsHonouredOnlyForNamedPeers(t *testing.T) {
	// fiber's test transport dials from 0.0.0.0; trust it explicitly.
	trusting := ipApp(t, Config{AppName: "svc", TrustProxy: true,
		TrustedProxies: []string{"0.0.0.0/0"}, ProxyHeader: "X-Forwarded-For"})
	if got := whoami(t, trusting, "203.0.113.9"); got != `{"ip":"203.0.113.9"}` {
		t.Errorf("a trusted proxy's forwarded address was not honoured: %s", got)
	}

	// The same app with an allowlist that does NOT include the peer falls back.
	notTrusting := ipApp(t, Config{AppName: "svc", TrustProxy: true,
		TrustedProxies: []string{"198.51.100.7"}, ProxyHeader: "X-Forwarded-For"})
	if got := whoami(t, notTrusting, "203.0.113.9"); got == `{"ip":"203.0.113.9"}` {
		t.Error("a forwarded address was believed from a peer that is not in the allowlist")
	}
}

// No connection behind the context means nothing is claimed — the same honest
// answer every other Caller field gives, and never a panic.
func TestCallerIP_EmptyWithNoConnection(t *testing.T) {
	if got := CallerOf(context.Background()).IP; got != "" {
		t.Errorf("CallerOf(background).IP = %q, want empty", got)
	}
}
