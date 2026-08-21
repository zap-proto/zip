// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip

import fiber "github.com/zap-proto/fiber/v3"

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

// linkHandler returns the middleware that names this app's own description on
// every answer, or nil when there is nothing to name.
//
// The condition is the SAME map installOpenAPIRoutes writes, not a second guess
// at it: the header is present exactly when the address it points at is served,
// so a link can never advertise a document this app 404s. An app whose document
// is Disabled, or which registers no typed op, gets no middleware at all rather
// than one that checks a flag on every request.
func (a *App) linkHandler() fiber.Handler {
	if !a.controls[fiber.MethodGet+" "+SpecPath] {
		return nil
	}
	// Built once, at materialise: these two are constants of the app, and
	// re-rendering them per request would allocate on every answer.
	desc := "<" + SpecPath + `>; rel="` + relDesc + `"`
	doc := "<" + DocsPath + `>; rel="` + relDoc + `"`
	return func(fc fiber.Ctx) error {
		// After the handler, so a handler's own Link header is already in place
		// and these join it rather than racing it. fiber buffers headers until
		// the chain returns, so writing here still reaches the client.
		err := fc.Next()
		h := &fc.Response().Header
		h.Add(fiber.HeaderLink, "<"+fc.Path()+`>; rel="`+relSelf+`"`)
		h.Add(fiber.HeaderLink, desc)
		h.Add(fiber.HeaderLink, doc)
		return err
	}
}
