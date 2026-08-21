package zip_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// answered drives one request and returns what arrived: status, headers, and
// the body already parsed. Every test here reads the WIRE — a rendering asserted
// against the function that produced it proves only that the function is itself.
func answered(t *testing.T, app *zip.App, method, path string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("%s %s: body is not JSON: %s", method, path, raw)
	}
	return res, body
}

func refusalApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "refusals", DisableStartupMessage: true})
	app.Use(middleware.Recover())
	app.Get("/thing/:id", func(c *zip.Ctx) error { return c.JSON(200, map[string]any{"ok": true}) })
	app.Put("/thing/:id", func(c *zip.Ctx) error { return c.JSON(200, map[string]any{"ok": true}) })
	app.Get("/panic", func(c *zip.Ctx) error { panic("nil map write") })
	app.Get("/capped", func(c *zip.Ctx) error {
		return zip.ErrPaymentRequired("spend cap exceeded").
			With(map[string]any{"cap": 5000, "spent": 5127})
	})

	oauth := zip.OAuth(app)
	oauth.Post("/v1/oauth/token", func(c *zip.Ctx) error {
		return &zip.HTTPError{Status: 400, Code: "invalid_grant", Msg: "code already redeemed"}
	})
	oauth.Post("/v1/oauth/introspect", func(c *zip.Ctx) error { panic("store closed") })
	return app
}

// TestARefusalIsAProblemDocument is RFC 9457 on the wire: the registered media
// type, and the five members a client that has never seen this service can read.
func TestARefusalIsAProblemDocument(t *testing.T) {
	res, body := answered(t, refusalApp(t), "GET", "/capped")

	if res.StatusCode != 402 {
		t.Fatalf("status = %d, want 402", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json — the suffix is what says the members mean what RFC 9457 says", ct)
	}
	for member, want := range map[string]any{
		"type":   "about:blank",
		"title":  "Payment Required",
		"status": float64(402),
		"detail": "spend cap exceeded",
	} {
		if body[member] != want {
			t.Errorf("%s = %v, want %v", member, body[member], want)
		}
	}
	// Extension members sit BESIDE the document's own (§3.2), not nested under a
	// key a client would have to be told about.
	if body["cap"] != float64(5000) || body["spent"] != float64(5127) {
		t.Errorf("extension members did not merge: %v", body)
	}
}

// TestAWrongVerbSaysWhichVerbsWork is RFC 9110 §15.5.6: a 405 MUST carry Allow.
// Without it a client cannot tell a wrong verb from a wrong address, and the two
// have opposite fixes.
func TestAWrongVerbSaysWhichVerbsWork(t *testing.T) {
	res, body := answered(t, refusalApp(t), "DELETE", "/thing/7")

	if res.StatusCode != 405 {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
	allow := res.Header.Values("Allow")
	if len(allow) == 0 {
		t.Fatal("no Allow header on a 405")
	}
	for _, want := range []string{"GET", "HEAD", "PUT"} {
		found := false
		for _, got := range allow {
			for _, m := range splitList(got) {
				if m == want {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("Allow %v omits %s, which the address does serve", allow, want)
		}
	}
	for _, m := range []string{"DELETE", "POST", "PATCH"} {
		for _, got := range allow {
			for _, listed := range splitList(got) {
				if listed == m {
					t.Errorf("Allow %v lists %s, which the address does not serve", allow, m)
				}
			}
		}
	}
	if body["status"] != float64(405) || body["title"] != "Method Not Allowed" {
		t.Errorf("405 body is not a problem document: %v", body)
	}
}

func splitList(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		switch r {
		case ',', ' ':
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		default:
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// TestAPanicIsAProblemDocument pins that a refusal nobody wrote leaves by the
// same door as one somebody did — Recover returns it rather than writing a body
// of its own, so it gets the media type and the vocabulary the address answers
// in.
func TestAPanicIsAProblemDocument(t *testing.T) {
	res, body := answered(t, refusalApp(t), "GET", "/panic")

	if res.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	if body["status"] != float64(500) {
		t.Errorf("panic did not render as a problem document: %v", body)
	}
}

// TestTheOAuthFamilyKeepsItsOwnVocabulary is the carve-out, and it is not a
// preference: RFC 6749 §5.2 states {error, error_description} under
// application/json, and RFC 7662 §2.3 and RFC 7009 §2.2.1 point at that section.
// A problem document here is a broken authorization server.
func TestTheOAuthFamilyKeepsItsOwnVocabulary(t *testing.T) {
	res, body := answered(t, refusalApp(t), "POST", "/v1/oauth/token")

	if res.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json — RFC 6749 §5.2", ct)
	}
	if body["error"] != "invalid_grant" || body["error_description"] != "code already redeemed" {
		t.Errorf("token endpoint answered %v, which no OAuth client parses", body)
	}
	for _, member := range []string{"type", "title", "detail", "status"} {
		if _, ok := body[member]; ok {
			t.Errorf("problem member %q leaked into an OAuth error", member)
		}
	}
}

// TestAnOAuthAddressSpeaksOAuthForRefusalsItDidNotWrite is why the vocabulary is
// a property of the ADDRESS and not of the returned value. A panic, a body that
// will not bind, a gate refusing in front of the handler: no handler is there to
// choose a shape for any of them, and they are exactly the requests where a
// client is already confused.
func TestAnOAuthAddressSpeaksOAuthForRefusalsItDidNotWrite(t *testing.T) {
	res, body := answered(t, refusalApp(t), "POST", "/v1/oauth/introspect")

	if res.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", res.StatusCode)
	}
	if body["error"] != "server_error" {
		t.Errorf("error = %v, want the RFC 6749 code for a failure that is the server's own", body["error"])
	}
	if _, ok := body["type"]; ok {
		t.Errorf("a panic at an OAuth address answered a problem document: %v", body)
	}
}

// TestAWrongVerbAtAnOAuthAddressIsTheRouterRefusing draws the line: the
// vocabulary belongs to the METHOD+path pair that was registered, so a verb the
// endpoint does not serve is the router's refusal and carries Allow, which is
// the answer the client actually needs.
func TestAWrongVerbAtAnOAuthAddressIsTheRouterRefusing(t *testing.T) {
	res, body := answered(t, refusalApp(t), "GET", "/v1/oauth/token")

	if res.StatusCode != 405 {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
	if got := res.Header.Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want POST", got)
	}
	if body["status"] != float64(405) {
		t.Errorf("405 body is not a problem document: %v", body)
	}
}

// TestTheVocabularySurvivesComposition is the reason it rides the route entry
// and not a table of addresses: a definition included under a host answers at a
// path it did not know it would have.
func TestTheVocabularySurvivesComposition(t *testing.T) {
	auth := zip.New(zip.Config{AppName: "auth", DisableStartupMessage: true})
	oauth := zip.OAuth(auth)
	oauth.Post("/token", func(c *zip.Ctx) error { return zip.ErrUnauthorized("bad secret") })

	host := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	host.Group("/v1/iam/oauth").Use(auth)

	res, body := answered(t, host, "POST", "/v1/iam/oauth/token")

	if res.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	if body["error"] != "invalid_client" || body["error_description"] != "bad secret" {
		t.Errorf("the composed token endpoint answered %v — the vocabulary did not follow the route", body)
	}
}

// TestAGroupOfTheOAuthRouterIsStillOAuth pins that the vocabulary descends the
// way a scoped chain does. A nested scope answering its parent's neighbours'
// shape is the same silently-lost property as a gate dropped at the second
// nesting, and just as invisible until a client breaks.
func TestAGroupOfTheOAuthRouterIsStillOAuth(t *testing.T) {
	app := zip.New(zip.Config{AppName: "nested-oauth", DisableStartupMessage: true})
	zip.OAuth(app).Group("/v1/oauth").Post("/revoke", func(c *zip.Ctx) error {
		return zip.ErrBadRequest("token is required")
	})

	res, body := answered(t, app, "POST", "/v1/oauth/revoke")

	if res.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if body["error"] != "invalid_request" || body["error_description"] != "token is required" {
		t.Errorf("a group of the OAuth router answered %v — the vocabulary did not descend", body)
	}
}

// TestOAuthTakesTypedOpsToo pins that the carve-out is a Router in full: an op
// declared through it is registered, documented and refused the same way an
// untyped handler is.
func TestOAuthTakesTypedOpsToo(t *testing.T) {
	app := zip.New(zip.Config{AppName: "typed-oauth", DisableStartupMessage: true})
	type in struct {
		Token string `json:"token"`
	}
	type out struct {
		Active bool `json:"active"`
	}
	zip.Post(zip.OAuth(app), "/v1/oauth/revoke", func(_ context.Context, i *in) (*out, error) {
		return nil, zip.ErrBadRequest("token is required")
	})

	res, body := answered(t, app, "POST", "/v1/oauth/revoke")

	if res.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if body["error"] != "invalid_request" || body["error_description"] != "token is required" {
		t.Errorf("typed op at an OAuth address answered %v", body)
	}
	if len(app.Routes()) != 1 || app.Routes()[0].Op == "" {
		t.Errorf("the op was not registered as an op: %v", app.Routes())
	}
}

// A REFUSAL SAYS NOTHING ABOUT WHERE IT HAPPENED, so two addresses that refuse
// for the same reason answer identically. That is what lets "a real name and an
// invented one are answered the same" be compared strictly rather than loosened
// until it passes. RFC 9457 §3.1.4 makes `instance` optional; filling it with the
// request path would tell the caller what the caller just said.
func TestTwoAddressesRefuseIdentically(t *testing.T) {
	a := zip.New(zip.Config{AppName: "same", DisableStartupMessage: true})
	deny := func(c *zip.Ctx) error { return zip.ErrNotFound("no such thing") }
	a.Get("/thing/real", deny)
	a.Get("/quite/another", deny)
	if err := a.Build(); err != nil {
		t.Fatal(err)
	}
	one, oneBody := answered(t, a, "GET", "/thing/real")
	two, twoBody := answered(t, a, "GET", "/quite/another")
	if one.StatusCode != 404 || two.StatusCode != 404 {
		t.Fatalf("status = %d and %d, want 404", one.StatusCode, two.StatusCode)
	}
	if _, ok := oneBody["instance"]; ok {
		t.Errorf("the document names its occurrence: %v", oneBody)
	}
	x, _ := json.Marshal(oneBody)
	y, _ := json.Marshal(twoBody)
	if string(x) != string(y) {
		t.Errorf("two addresses refused differently:\n  %s\n  %s", x, y)
	}
}
