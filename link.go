// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

import (
	"bytes"

	"github.com/valyala/fasthttp"
	fiber "github.com/zap-proto/fiber/v3"
)

// EVERY ANSWER SAYS WHERE TO LEARN WHAT ELSE THIS API SERVES.
//
// An API a client can only use by reading documentation somewhere else is one
// whose callers hard-code addresses, and hard-coded addresses are what makes a
// rename a breakage. RFC 8288 is the standard answer — a `Link` header naming a
// target and the RELATION it stands in to this resource — and RFC 8631 registers
// the two relations an API needs: `service-desc` for the machine-readable
// description, `service-doc` for the page a person reads. zip already serves
// both (SpecPath, DocsPath); until now nothing said so, so a client had to
// already know they existed to find them.
//
// It is HEADERS and not a body, deliberately. A body shape is the resource's
// own — one app answers a bare array, another an envelope — so writing links
// into it would mean zip choosing a representation for every handler in the
// estate. A header is beside the representation rather than inside it, so this
// applies to a JSON object, a byte stream and a 404 alike.
//
// `Link` is a LIST header (RFC 8288 §3), so these are ADDED and never set: a
// handler with links of its own — a page naming `next` and `prev` — keeps them,
// and both sets arrive at the client.
const (
	// relSelf names the address the caller actually reached, which is the one
	// fact a client cannot reconstruct: it may have arrived through a proxy, an
	// alias, or a redirect.
	relSelf = "self"
	// relDesc and relDoc are RFC 8631. Registered relations rather than invented
	// ones, so a generic client — a crawler, an SDK generator, an agent — follows
	// them without being taught anything specific to us.
	relDesc = "service-desc"
	relDoc  = "service-doc"
)

// linkHandler returns the middleware that names an answer's own address, and
// this app's description alongside it when there is one.
//
// SELF IS TRUE OF EVERY ANSWER. It is the path the request arrived on, and
// nothing has to be installed for it to be so. Only the description relations
// depend on a document existing, so only they are conditional — on the SAME map
// installOpenAPIRoutes writes, not a second guess at it, so a link can never
// advertise a document this app 404s.
//
// The two used to be one condition, and that cost more than the description:
// installOpenAPIRoutes returns early for an app that registers no typed op, so
// every such app — and most of ours are, serving plain routes — named nothing
// at all, not even itself. Whether an app happens to use typed ops is not a
// statement about whether its answers have addresses.
func (a *App) linkHandler() fiber.Handler {
	// Built once, at materialise: these are constants of the app, and
	// re-rendering them per request would allocate on every answer.
	var described []string
	if a.controls[fiber.MethodGet+" "+SpecPath] {
		described = []string{
			"<" + SpecPath + `>; rel="` + relDesc + `"`,
			"<" + DocsPath + `>; rel="` + relDoc + `"`,
		}
	}
	return func(fc fiber.Ctx) error {
		// After the handler, so a handler's own Link header is already in place
		// and these join it rather than racing it. fiber buffers headers until
		// the chain returns, so writing here still reaches the client.
		err := fc.Next()
		h := &fc.Response().Header
		// Once per ANSWER, not once per app that touches it. A proxied answer is
		// written by the far end INTO THIS RESPONSE, links and all, so a request
		// that crosses a hop arrived here already carrying these — and the far
		// end is the app that actually served, so its account is the true one.
		// Measured in production: three self links on one answer, two of them
		// this middleware running on both sides of a hop.
		if hasSelf(h) {
			return err
		}
		h.Add(fiber.HeaderLink, "<"+fc.Path()+`>; rel="`+relSelf+`"`)
		for _, l := range described {
			h.Add(fiber.HeaderLink, l)
		}
		return err
	}
}

// hasSelf reports whether a self link is already on the answer. It reads the
// HEADER rather than keeping a flag beside it, because the header is the thing
// that must not be written twice — a flag would be a second account of it, and
// the two could disagree across a hop, which is the only place this matters.
func hasSelf(h *fasthttp.ResponseHeader) bool {
	found := false
	h.VisitAll(func(k, v []byte) {
		if found || !bytes.EqualFold(k, []byte(fiber.HeaderLink)) {
			return
		}
		if bytes.Contains(v, []byte(`rel="`+relSelf+`"`)) {
			found = true
		}
	})
	return found
}
