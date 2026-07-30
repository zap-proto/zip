package zip_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zap-proto/zip"
)

type askIn struct {
	Ref string `json:"ref"`
}
type askOut struct {
	Org string `json:"org"`
}

// Ask is the whole of what a caller needs to reach a peer: a name, an op token
// and a value. No address, no registry, no discovery — and the tenant rides the
// CALLER, so the callee reads an org the caller could not choose.
func TestAskReachesAPeerByNameAndCarriesTheCaller(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(zip.RuntimeDirEnv, dir)

	peer := zip.New(zip.Config{AppName: "ledger", DisableStartupMessage: true})
	zip.Post(peer, "/v1/ledger/whoami", func(ctx context.Context, in *askIn) (*askOut, error) {
		org, ok := zip.Tenant(ctx)
		if !ok {
			return nil, zip.Errorf(403, "no tenant")
		}
		return &askOut{Org: org}, nil
	}, zip.WithOperationID("ledger_whoami"))

	go func() { _ = peer.Listen(zip.SocketPath("ledger")) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	waitSock(t, zip.SocketPath("ledger"))

	// A background job STATES its caller once, explicitly. It cannot launder one:
	// an inbound request always wins over a stated caller.
	ctx := zip.WithCaller(context.Background(), zip.Caller{User: "u_job", Org: "acme"})
	out, err := zip.Ask[askIn, askOut](ctx, "ledger", "ledger_whoami", &askIn{Ref: "r1"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if out.Org != "acme" {
		t.Fatalf("org = %q, want acme", out.Org)
	}

	// A caller with no validated principal is refused by the CALLEE, not by
	// anything the caller could skip.
	_, err = zip.Ask[askIn, askOut](context.Background(), "ledger", "ledger_whoami", &askIn{})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("unauthenticated Ask = %v, want a 403 from the callee", err)
	}
}

// The Conn is kept for the life of the process: a plane hop must not pay for a
// fresh connect, which is what "dial, call, close" per call costs.
func TestAskReusesOneConnPerPeer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(zip.RuntimeDirEnv, dir)

	peer := zip.New(zip.Config{AppName: "reuse", DisableStartupMessage: true})
	zip.Post(peer, "/v1/reuse/ping", func(ctx context.Context, in *askIn) (*askOut, error) {
		return &askOut{Org: "ok"}, nil
	}, zip.WithOperationID("reuse_ping"))
	go func() { _ = peer.Listen(zip.SocketPath("reuse")) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	waitSock(t, zip.SocketPath("reuse"))

	ctx := zip.WithCaller(context.Background(), zip.Caller{User: "u", Org: "o"})
	for i := 0; i < 20; i++ {
		if _, err := zip.Ask[askIn, askOut](ctx, "reuse", "reuse_ping", &askIn{}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// One socket, N calls: nothing to assert about the pool from outside, so the
	// property under test is that 20 sequential calls succeed on one handle
	// without the caller ever holding a Conn.
}
