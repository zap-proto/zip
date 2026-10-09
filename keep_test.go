package zip_test

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"

	"github.com/zap-proto/zip"
)

// A string read off a request is the handler's to keep.
//
// A server answers every request on a connection from one fasthttp.RequestCtx
// and parses each request into the buffers the one before it used; fiber's own
// context is pooled the same way. A value that is a VIEW of those buffers is
// rewritten in place when the next request on the connection carries a value
// of the same length, so a handler that keeps one past its return — a map key,
// a cache entry, a goroutine, a usage record written after the answer — later
// reads the next caller's. In hanzo-inc/cloud that handed one API-key caller
// another tenant's principal.
//
// Each case keeps everything a handler can read off a request, sends a second
// request on the SAME connection with different values of the same length,
// and requires the first request's values to read as they did. Over both
// transports, because both serve a connection from one reused RequestCtx.

type keepIn struct {
	ID   string `json:"id"`
	Q    string `json:"q"`
	Auth string `json:"auth" header:"Authorization"`
	Name string `json:"name"`
}

type keepOut struct{}

// keepForm is a typed op's form, cookie and header, the request kinds that do
// not come from a JSON body.
type keepForm struct {
	F      string    `json:"f" form:"f"`
	Sid    string    `json:"sid" cookie:"sid"`
	Auth   string    `json:"auth" header:"Authorization"`
	Upload *zip.File `json:"upload" form:"upload"`
}

// keepBytes is a typed op's raw body.
type keepBytes struct {
	Body zip.Body `json:"body"`
}

// keeper holds what each request's handler kept, in arrival order.
type keeper struct {
	mu   sync.Mutex
	seen []map[string]string
	body [][]byte
}

func (k *keeper) keep(m map[string]string, body []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seen = append(k.seen, m)
	k.body = append(k.body, body)
}

func (k *keeper) at(i int) (map[string]string, []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.seen[i], k.body[i]
}

// val is field's value in request i: the same length for every i.
func val(field string, i int) string {
	return field + "-" + strings.Repeat(string(rune('a'+i)), 12)
}

func keepApp(k *keeper) *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw("POST", "/v1/raw/:id", func(c *zip.Ctx) error {
		var q struct {
			Q string `query:"q"`
		}
		if err := c.BindQuery(&q); err != nil {
			return err
		}
		var f struct {
			F string `form:"f"`
		}
		if err := c.Bind(&f); err != nil {
			return err
		}
		var m map[string]string
		if err := c.BindQuery(&m); err != nil {
			return err
		}
		var u struct {
			ID string `uri:"id"`
		}
		if err := c.BindURI(&u); err != nil {
			return err
		}
		fc := c.Fiber()
		k.keep(map[string]string{
			"header":     c.Header("Authorization"),
			"org":        c.Org(),
			"user":       c.User(),
			"query":      c.Query("q"),
			"param":      c.Param("id"),
			"path":       c.Path(),
			"cookie":     fc.Cookies("sid"),
			"queries":    fc.Queries()["q"],
			"reqheaders": fc.GetReqHeaders()["Authorization"][0],
			"formvalue":  fc.FormValue("f"),
			"bindquery":  q.Q,
			"bindmap":    m["q"],
			"bindform":   f.F,
			"binduri":    u.ID,
			"caller":     zip.CallerOf(c.Forward()).Org,
		}, c.Body())
		return c.NoContent(204)
	})
	app.Raw("POST", "/v1/json", func(c *zip.Ctx) error {
		var in struct {
			Name string            `json:"name"`
			Tags []string          `json:"tags"`
			Meta map[string]string `json:"meta"`
			Any  any               `json:"any"`
		}
		if err := c.Bind(&in); err != nil {
			return err
		}
		k.keep(map[string]string{
			"name": in.Name,
			"tag":  in.Tags[0],
			"meta": in.Meta["k"],
			"any":  in.Any.(map[string]any)["k"].(string),
		}, nil)
		return c.NoContent(204)
	})
	app.Post("/v1/typed/:id", func(ctx context.Context, in *keepIn) (*keepOut, error) {
		k.keep(map[string]string{
			"param":  in.ID,
			"query":  in.Q,
			"header": in.Auth,
			"body":   in.Name,
			"caller": zip.CallerOf(ctx).Org,
			"read":   zip.Header(ctx, "Authorization"),
		}, nil)
		return nil, nil
	})
	app.Post("/v1/form", func(_ context.Context, in *keepForm) (*keepOut, error) {
		k.keep(map[string]string{
			"form":   in.F,
			"cookie": in.Sid,
			"header": in.Auth,
		}, nil)
		return nil, nil
	}, zip.Consumes("application/x-www-form-urlencoded"))
	app.Post("/v1/upload", func(_ context.Context, in *keepForm) (*keepOut, error) {
		k.keep(map[string]string{
			"form": in.F,
			"name": in.Upload.Name,
			"type": in.Upload.Type,
		}, in.Upload.Bytes)
		return nil, nil
	}, zip.Consumes("multipart/form-data"))
	app.Post("/v1/bytes", func(_ context.Context, in *keepBytes) (*keepOut, error) {
		k.keep(map[string]string{"type": in.Body.Type}, in.Body.Bytes)
		return nil, nil
	})
	return app
}

// formAsk is request i to the typed form op.
func formAsk(i int) *fasthttp.Request {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod("POST")
	req.SetRequestURI("http://keep/v1/form")
	req.Header.Set("Authorization", "Bearer "+val("header", i))
	req.Header.SetCookie("sid", val("cookie", i))
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.SetBodyString("f=" + val("form", i))
	return req
}

func formWant(i int) map[string]string {
	return map[string]string{
		"form":   val("form", i),
		"cookie": val("cookie", i),
		"header": "Bearer " + val("header", i),
	}
}

// uploadAsk is request i to the typed multipart op: one field and one part.
func uploadAsk(i int) *fasthttp.Request {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod("POST")
	req.SetRequestURI("http://keep/v1/upload")
	req.Header.SetContentType("multipart/form-data; boundary=keepboundary")
	req.SetBodyString("--keepboundary\r\n" +
		"Content-Disposition: form-data; name=\"f\"\r\n\r\n" + val("form", i) + "\r\n" +
		"--keepboundary\r\n" +
		"Content-Disposition: form-data; name=\"upload\"; filename=\"" + val("name", i) + "\"\r\n" +
		"Content-Type: text/" + val("type", i) + "\r\n\r\n" + val("part", i) + "\r\n" +
		"--keepboundary--\r\n")
	return req
}

func uploadWant(i int) map[string]string {
	return map[string]string{
		"form": val("form", i),
		"name": val("name", i),
		"type": "text/" + val("type", i),
	}
}

// bytesAsk is request i to the typed raw-body op.
func bytesAsk(i int) *fasthttp.Request {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod("POST")
	req.SetRequestURI("http://keep/v1/bytes")
	req.Header.SetContentType("application/" + val("media", i))
	req.SetBodyString(val("bytes", i))
	return req
}

func bytesWant(i int) map[string]string {
	return map[string]string{"type": "application/" + val("media", i)}
}

// rawAsk is request i to the untyped route: every value it carries is val(…, i).
func rawAsk(i int) *fasthttp.Request {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod("POST")
	req.SetRequestURI("http://keep/v1/raw/" + val("param", i) + "?q=" + val("query", i))
	req.Header.Set("Authorization", "Bearer "+val("header", i))
	req.Header.Set(zip.HeaderOrg, val("org", i))
	req.Header.Set(zip.HeaderUser, val("user", i))
	req.Header.SetCookie("sid", val("cookie", i))
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.SetBodyString("f=" + val("form", i))
	return req
}

// rawWant is what request i's handler must still read after later requests.
func rawWant(i int) map[string]string {
	return map[string]string{
		"header":     "Bearer " + val("header", i),
		"org":        val("org", i),
		"user":       val("user", i),
		"query":      val("query", i),
		"param":      val("param", i),
		"path":       "/v1/raw/" + val("param", i),
		"cookie":     val("cookie", i),
		"queries":    val("query", i),
		"reqheaders": "Bearer " + val("header", i),
		"formvalue":  val("form", i),
		"bindquery":  val("query", i),
		"bindmap":    val("query", i),
		"bindform":   val("form", i),
		"binduri":    val("param", i),
		"caller":     val("org", i),
	}
}

// jsonAsk is request i to the untyped route that binds a JSON body.
func jsonAsk(i int) *fasthttp.Request {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod("POST")
	req.SetRequestURI("http://keep/v1/json")
	req.Header.SetContentType("application/json")
	req.SetBodyString(`{"name":"` + val("name", i) + `","tags":["` + val("tag", i) + `"],"meta":{"k":"` +
		val("meta", i) + `"},"any":{"k":"` + val("any", i) + `"}}`)
	return req
}

func jsonWant(i int) map[string]string {
	return map[string]string{
		"name": val("name", i),
		"tag":  val("tag", i),
		"meta": val("meta", i),
		"any":  val("any", i),
	}
}

func typedAsk(i int) *fasthttp.Request {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod("POST")
	req.SetRequestURI("http://keep/v1/typed/" + val("param", i) + "?q=" + val("query", i))
	req.Header.Set("Authorization", "Bearer "+val("header", i))
	req.Header.Set(zip.HeaderOrg, val("org", i))
	req.Header.SetContentType("application/json")
	req.SetBodyString(`{"name":"` + val("body", i) + `"}`)
	return req
}

func typedWant(i int) map[string]string {
	return map[string]string{
		"param":  val("param", i),
		"query":  val("query", i),
		"header": "Bearer " + val("header", i),
		"body":   val("body", i),
		"caller": val("org", i),
		"read":   "Bearer " + val("header", i),
	}
}

// keepServe serves app over HTTP/1.1 and ZAP and returns a client for each
// that holds ONE connection, so every request it sends reuses the server's
// RequestCtx for that connection.
func keepServe(t *testing.T, app *zip.App) map[string]zip.Client {
	t.Helper()
	addr := free(t)
	sock := filepath.Join(sockDir(t), "keep.sock")
	h, err := zip.Serve(app, "http://"+addr, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	waitTCP(t, addr)
	waitSock(t, sock)
	return map[string]zip.Client{
		"http": &fasthttp.HostClient{Addr: addr, MaxConns: 1},
		"zap":  zaphttp.Dial("unix", sock),
	}
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	for range 100 {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never began listening", addr)
}

func TestKeep_AStringKeptPastTheHandlerIsNotTheNextRequests(t *testing.T) {
	k := &keeper{}
	clients := keepServe(t, keepApp(k))
	cases := []struct {
		route string
		ask   func(int) *fasthttp.Request
		want  func(int) map[string]string
		body  func(int) string
	}{
		{"raw", rawAsk, rawWant, func(i int) string { return "f=" + val("form", i) }},
		{"json", jsonAsk, jsonWant, nil},
		{"typed", typedAsk, typedWant, nil},
		{"form", formAsk, formWant, nil},
		{"upload", uploadAsk, uploadWant, func(i int) string { return val("part", i) }},
		{"bytes", bytesAsk, bytesWant, func(i int) string { return val("bytes", i) }},
	}
	for _, tr := range []string{"http", "zap"} {
		for _, tc := range cases {
			t.Run(tr+"/"+tc.route, func(t *testing.T) {
				client := clients[tr]
				k.mu.Lock()
				first := len(k.seen)
				k.mu.Unlock()
				// Request 0 is kept; requests 1-3 reuse its buffers.
				for i := range 4 {
					req, resp := tc.ask(i), fasthttp.AcquireResponse()
					if err := client.Do(req, resp); err != nil {
						t.Fatalf("request %d: %v", i, err)
					}
					if resp.StatusCode() != 204 {
						t.Fatalf("request %d: status %d: %s", i, resp.StatusCode(), resp.Body())
					}
					fasthttp.ReleaseRequest(req)
					fasthttp.ReleaseResponse(resp)
				}
				for i := range 4 {
					got, body := k.at(first + i)
					for name, want := range tc.want(i) {
						if got[name] != want {
							t.Errorf("request %d's %s now reads %q, want %q", i, name, got[name], want)
						}
					}
					if tc.body != nil && string(body) != tc.body(i) {
						t.Errorf("request %d's body now reads %q, want %q", i, body, tc.body(i))
					}
				}
			})
		}
	}
}
