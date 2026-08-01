package zip

// Alias registers ONE handler at TWO addresses — the canonical one and a legacy
// spelling kept reachable for consumers pinned to it.
//
// A path segment names a THING; the HTTP method says what is being done to it.
// The verb-noun addresses a service inherits (`send-verification-code`,
// `set-preferred-mfa`, …) say the verb twice, and they are what a customer reads
// in a CLI's help, in every generated SDK method name and on every docs page. The
// canonical noun is what the published document leads with; the legacy spelling
// stays reachable so nothing breaks while consumers move.
//
// One handler VALUE, two addresses: there is no second implementation to keep in
// step, and no forward that could answer differently from the thing it forwards
// to. When the last pinned consumer moves, the legacy half is deleted and nothing
// else changes.
//
// It lives here rather than in each service because cmd/zipdoc has to recognise
// it. A registration made inside a helper is invisible to a pass that reads
// router.Get(path, handler) calls, so a service that rolled its own alias helper
// silently lost the prose for BOTH addresses — the exact defect this package
// exists to prevent. Being zip's, both halves carry the handler's doc comment.
func Alias(reg func(string, ...Handler) Router, canonical, legacy string, h Handler) {
	reg(canonical, h)
	reg(legacy, h)
}
