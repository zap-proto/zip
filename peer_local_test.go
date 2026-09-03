package zip_test

import (
	"context"
	"os"
	"testing"

	"github.com/zap-proto/zip"
)

type localIn struct{}
type localOut struct {
	Local bool `json:"local"`
	Peer  bool `json:"peer"`
}

// A call the process makes to itself has no peer and is Local; a call over the
// socket has a peer the kernel attested and is not.
func TestLocalSeparatesHereFromTheSocket(t *testing.T) {
	dir := sockDir(t)
	t.Setenv(zip.RuntimeDirEnv, dir)
	app := zip.New(zip.Config{AppName: "who"})
	zip.Post[localIn, localOut](app, "/who", func(ctx context.Context, _ *localIn) (*localOut, error) {
		return &localOut{Local: zip.Local(ctx), Peer: zip.PeerOf(ctx) != nil}, nil
	}, zip.WithOperationID("who"))
	sock := zip.SocketPath("who")
	go func() { _ = app.Listen(sock) }()
	waitFor(t, sock)

	here, err := zip.Here[localIn, localOut](context.Background(), app, "who", &localIn{})
	if err != nil {
		t.Fatal(err)
	}
	if !here.Local || here.Peer {
		t.Fatalf("Here: local=%v peer=%v, want local and no peer", here.Local, here.Peer)
	}
	conn, err := zip.DialApp("who")
	if err != nil {
		t.Fatal(err)
	}
	over, err := zip.Call[localIn, localOut](context.Background(), conn, "who", &localIn{})
	if err != nil {
		t.Fatal(err)
	}
	if over.Local || !over.Peer {
		t.Fatalf("socket: local=%v peer=%v, want a peer and not local", over.Local, over.Peer)
	}
}

// The socket is bound with the mode the config names, and 0600 when it names
// none.
func TestSocketModeIsTheConfigs(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  os.FileMode
		want os.FileMode
	}{{"shut", 0, 0o600}, {"open", 0o666, 0o666}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := sockDir(t)
			t.Setenv(zip.RuntimeDirEnv, dir)
			app := zip.New(zip.Config{AppName: "mode-" + tc.name, SocketMode: tc.cfg})
			sock := zip.SocketPath("mode-" + tc.name)
			go func() { _ = app.Listen(sock) }()
			waitFor(t, sock)
			st, err := os.Stat(sock)
			if err != nil {
				t.Fatal(err)
			}
			if got := st.Mode().Perm(); got != tc.want {
				t.Fatalf("socket mode %o, want %o", got, tc.want)
			}
		})
	}
}
