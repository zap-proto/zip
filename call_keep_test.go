package zip_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// A value that crosses the plane is its receiver's to keep. The bytes it was
// decoded from are not: the client reads a reply into a pooled fasthttp response
// that the next call reuses, and the server reads a request the same way. ZAP
// reads text zero-copy, so a string still pointing into those bytes turns into
// the next message while its owner holds it. The race detector cannot see it —
// the reuse is sequential, not concurrent — so the only proof is to keep a value
// across the next call and read it again.

type keepIn struct {
	Say  string `json:"say"`
	Fail bool   `json:"fail"`
}

type keepOut struct {
	Said string `json:"said"`
}

// keepRounds is how many first/second pairs a test makes. One pair is the
// bug's whole shape, but under -race sync.Pool drops a quarter of what is Put,
// so one pair can miss the reuse; eight make a miss 1 in 65,536.
const keepRounds = 8

// keepApp echoes what it is told, refuses with it when asked to, and keeps every
// input it was handed, as a handler that caches or queues its input does.
func keepApp(kept *[]string, mu *sync.Mutex) *zip.App {
	app := zip.New(zip.Config{AppName: "keep", DisableStartupMessage: true})
	zip.Post(app, "/v1/keep", func(_ context.Context, in *keepIn) (*keepOut, error) {
		mu.Lock()
		*kept = append(*kept, in.Say)
		mu.Unlock()
		if in.Fail {
			return nil, zip.ErrForbidden(in.Say)
		}
		return &keepOut{Said: in.Say}, nil
	}, zip.WithOperationID("keep_echo"))
	return app
}

func dialKeep(t *testing.T, app *zip.App) *zip.Conn {
	t.Helper()
	c, err := zip.Dial(serveUDS(t, app))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCall_ReplyOutlivesTheNextCall(t *testing.T) {
	var mu sync.Mutex
	var kept []string
	c := dialKeep(t, keepApp(&kept, &mu))
	ctx := context.Background()

	for i := 0; i < keepRounds; i++ {
		want := fmt.Sprintf("first-%02d", i)
		first, err := zip.Call[keepIn, keepOut](ctx, c, "keep_echo", &keepIn{Say: want})
		if err != nil {
			t.Fatalf("first call: %v", err)
		}
		if _, err := zip.Call[keepIn, keepOut](ctx, c, "keep_echo", &keepIn{Say: fmt.Sprintf("later-%02d", i)}); err != nil {
			t.Fatalf("second call: %v", err)
		}
		if first.Said != want {
			t.Fatalf("round %d: the first reply reads %q after the second call, want %q", i, first.Said, want)
		}
	}
}

// A refusal crosses as an *HTTPError decoded from the same pooled body, so its
// message is held to the same rule as a reply.
func TestCall_RefusalOutlivesTheNextCall(t *testing.T) {
	var mu sync.Mutex
	var kept []string
	c := dialKeep(t, keepApp(&kept, &mu))
	ctx := context.Background()

	for i := 0; i < keepRounds; i++ {
		want := fmt.Sprintf("first-%02d", i)
		_, err := zip.Call[keepIn, keepOut](ctx, c, "keep_echo", &keepIn{Say: want, Fail: true})
		var he *zip.HTTPError
		if !errors.As(err, &he) {
			t.Fatalf("first call: want an *HTTPError, got %#v", err)
		}
		if _, err := zip.Call[keepIn, keepOut](ctx, c, "keep_echo", &keepIn{Say: fmt.Sprintf("later-%02d", i), Fail: true}); err == nil {
			t.Fatal("second call: want a refusal")
		}
		if he.Msg != want {
			t.Fatalf("round %d: the first refusal reads %q after the second call, want %q", i, he.Msg, want)
		}
	}
}

// The server decodes In out of a pooled request body, which the next request
// reuses once the handler returns. A handler that keeps its input — a cache, a
// queue, a goroutine it starts — must still hold what it was sent.
func TestCall_HandlerMayKeepItsInput(t *testing.T) {
	var mu sync.Mutex
	var kept []string
	c := dialKeep(t, keepApp(&kept, &mu))
	ctx := context.Background()

	var sent []string
	for i := 0; i < 2*keepRounds; i++ {
		say := fmt.Sprintf("input-%02d", i)
		sent = append(sent, say)
		if _, err := zip.Call[keepIn, keepOut](ctx, c, "keep_echo", &keepIn{Say: say}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(kept, sent) {
		t.Fatalf("inputs the handler kept changed under later calls:\n got %q\nwant %q", kept, sent)
	}
}
