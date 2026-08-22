// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

import (
	"errors"
	"net/http"

	fiber "github.com/zap-proto/fiber/v3"
)

// A REFUSAL IS WRITTEN IN THE VOCABULARY ITS ADDRESS SPEAKS.
//
// RFC 9457 is the one an HTTP API answers in: a problem document under the
// registered media type application/problem+json, carrying `type`, `title`,
// `status` and `detail`, plus whatever extension members the
// refusal adds. A client that has never seen this service can read a 402 from
// it and know what happened, which is the entire point of a registered type —
// where `{"error":"..."}` is a shape you have to be told about first.
//
// OAuth is the exception, and it is not a matter of taste. RFC 6749 §5.2 states
// the token endpoint's error body as {error, error_description} under
// application/json; RFC 7662 §2.3 and RFC 7009 §2.2.1 point introspection and
// revocation at that same section. Every OAuth client parses those two members
// and nothing else. A problem document there is not a nicer error, it is a
// broken authorization server — so the family keeps its own vocabulary, and
// says so at registration: see [OAuth].
//
// Which one an address speaks is resolved ONCE, here, from the route the
// request matched — never from the error value. The refusals that most need
// translating are the ones no handler wrote: a body that would not bind, a
// panic, a middleware refusing in front of the handler. There is no return
// statement to attach a type to for any of those, and they are exactly the
// requests where a client is already confused.

// mimeProblem is RFC 9457 §3. The suffix is what tells a client the body is
// JSON AND that its members mean what the RFC says they mean.
const mimeProblem = "application/problem+json"

// problem renders a refusal as an RFC 9457 problem document.
//
// The optional `instance` member (RFC 9457 §3.1.4) is not written. It would
// name the address the refusal happened at — which the caller supplied and
// already holds, so it tells them nothing. What it does do is make two refusals
// at two addresses differ in their bodies, and the property worth testing about
// a refusal is that asking about a real resource and a fabricated one produce
// the SAME answer. Echoing the request path turns that comparison into one that
// can only pass by being loosened, so the member is left out.
//
// Extension members MERGE at the top level (RFC 9457 §3.2) rather than nesting
// under a key a reader would have to know to look in. The document's own
// members are written LAST, so a domain key called `status` cannot displace the
// refusal's.
func (e *HTTPError) problem() map[string]any {
	out := make(map[string]any, len(e.Detail)+6)
	for k, v := range e.Detail {
		out[k] = v
	}
	// RFC 9457 §3.1.1 — the type of a problem that carries no identity beyond
	// its status, and also the value assumed when the member is absent. Written
	// out rather than omitted so the document reads completely without the
	// reader holding the RFC.
	out["type"] = "about:blank"
	// §3.1.1 again: with that type, the title SHOULD be the status's own phrase.
	// A status with no registered phrase gets no title rather than an invented
	// one.
	if title := http.StatusText(e.Status); title != "" {
		out["title"] = title
	}
	out["status"] = e.Status
	out["detail"] = e.Msg
	if e.Code != "" {
		out["code"] = e.Code
	}
	return out
}

// oauth renders a refusal as RFC 6749 §5.2: the error CODE, and the human
// description beside it.
//
// [HTTPError] already carries both halves — Code is the token a client
// dispatches on, Msg the sentence a developer reads — so the same value serves
// both vocabularies and there is no second error type to keep in step.
func (e *HTTPError) oauth() map[string]any {
	out := make(map[string]any, len(e.Detail)+2)
	for k, v := range e.Detail {
		out[k] = v
	}
	code := e.Code
	if code == "" {
		code = oauthCode(e.Status)
	}
	out["error"] = code
	if e.Msg != "" {
		out["error_description"] = e.Msg
	}
	return out
}

// oauthCode names a refusal the framework produced, where no handler chose a
// code. RFC 6749 §5.2 pairs invalid_client with 401 specifically and makes
// invalid_request the answer for a request that is malformed in any other way;
// server_error is the registry entry (RFC 6749 §4.1.2.1) for a failure that is
// the server's own.
func oauthCode(status int) string {
	switch {
	case status == 401:
		return "invalid_client"
	case status >= 500:
		return "server_error"
	default:
		return "invalid_request"
	}
}

// refusing is the app's error handler, closed over the addresses that speak
// RFC 6749. The map is built with the router (see [composeOAuth]) and read only
// once a request has already failed, so an app declaring no OAuth surface
// carries a nil map and pays one nil-map lookup on the error path.
func refusing(oauth map[string]bool) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		e := refusal(err)
		c.Status(e.Status)
		if oauth[c.Method()+" "+c.Route().Path] {
			return c.JSON(e.oauth(), mimeJSON)
		}
		return c.JSON(e.problem(), mimeProblem)
	}
}

// refusal is the [HTTPError] an error stands for: the one it carries, fiber's
// own with the status it chose, or a 500 for anything else.
//
// A refusal that names no status is answered as a copy rather than by filling
// the original in, because a package-level `var ErrGone = zip.ErrNotFound(…)`
// is one value shared by every request that returns it.
func refusal(err error) *HTTPError {
	var he *HTTPError
	if errors.As(err, &he) {
		if he.Status != 0 {
			return he
		}
		return &HTTPError{Status: 500, Code: he.Code, Msg: he.Msg, Detail: he.Detail}
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return &HTTPError{Status: fe.Code, Msg: fe.Message}
	}
	return &HTTPError{Status: 500, Msg: err.Error()}
}

// OAuth is where the RFC 6749 family is registered: the token, introspection
// and revocation endpoints, whose refusals are {error, error_description} and
// whose neighbours' are problem documents.
//
//	oauth := zip.OAuth(r)
//	oauth.Post("/v1/oauth/token", token)
//	oauth.Post("/v1/oauth/introspect", introspect)
//	oauth.Post("/v1/oauth/revoke", revoke)
//
// It returns a Router, so it takes typed ops the same way — zip.Post(oauth, …)
// — and nests: a Group of it is still OAuth's. The vocabulary rides the route
// ENTRY from here (see [App.addRoute]), which is what makes it survive
// composition: a service composed under a host answers the same way at its new
// path, where an address recorded at registration would name a path that no
// longer exists.
//
// It covers the whole address and not just the handler, because the refusals
// that must obey RFC 6749 include the ones the handler never sees — a body over
// the limit, a panic recovered above it, a gate refusing in front of it.
func OAuth(on OpTarget) Router {
	s := on.OpScope()
	g := s.App.group(here(1), s.Prefix)
	g.wrap = s.Middleware
	g.oauth = true
	return g
}

// composeOAuth reduces a walk to the addresses whose refusals speak RFC 6749,
// in the absolute spelling the router matched on — the same projection the
// declaration and the op registry are, over the same occurrence slice.
func composeOAuth(occ []occurrence) map[string]bool {
	var out map[string]bool
	for _, o := range occ {
		r, ok := o.route()
		if !ok || !r.oauth || r.method == "" {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		for _, m := range declaredMethods(r.method) {
			out[m+" "+joinPath(o.ctx.prefix, r.path)] = true
		}
	}
	return out
}
