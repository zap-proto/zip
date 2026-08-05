package zip_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// A COMMAND ADDRESSES ONE OPERATION, and its arguments may not change which.
//
// Invoke percent-encodes every path argument for exactly this reason, but the
// encoding is only worth as much as the transport that carries it: fasthttp's
// default serialisation re-reads the path through Path(), which decodes %2F and
// resolves "..", so an argument could retarget the request at any route the
// method reached. do() disables that normalising; this is the pin.
func TestRemoteInvoke_ArgumentsCannotRetargetThePath(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cmd := zip.Command{
		Service: "platform", Name: "apps-get",
		Method: http.MethodGet, Path: "/v1/platform/apps/:app",
		Args: []zip.Arg{{Name: "app"}},
	}
	remote := zip.Remote{Base: srv.URL}

	cases := []struct {
		arg  string
		want string
	}{
		{"web", "/v1/platform/apps/web"},
		{"../../../v1/iam/users", "/v1/platform/apps/..%2F..%2F..%2Fv1%2Fiam%2Fusers"},
		{"a/b", "/v1/platform/apps/a%2Fb"},
		{"a%2Fb", "/v1/platform/apps/a%252Fb"},
		{"..", "/v1/platform/apps/.."},
	}
	for _, c := range cases {
		if _, err := remote.Invoke(context.Background(), cmd, map[string]string{"app": c.arg}, nil); err != nil {
			t.Fatalf("arg %q: %v", c.arg, err)
		}
		if got != c.want {
			t.Errorf("arg %q reached %q, want %q", c.arg, got, c.want)
		}
	}
}

// The query half is unaffected: a bodyless op's flags still ride the URL.
func TestRemoteInvoke_QuerySurvives(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.EscapedPath() + "?" + r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cmd := zip.Command{
		Service: "o11y", Name: "logs-list",
		Method: http.MethodGet, Path: "/v1/o11y/logs",
	}
	remote := zip.Remote{Base: srv.URL}
	if _, err := remote.Invoke(context.Background(), cmd, nil, []byte(`{"limit":5}`)); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got != "/v1/o11y/logs?limit=5" {
		t.Fatalf("reached %q", got)
	}
}
