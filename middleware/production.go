package middleware

import "github.com/zap-proto/zip"

// hstsPolicy is the Strict-Transport-Security value: two years, subdomains
// included. Matches the Stripe/Cloudflare/GitHub production posture.
const hstsPolicy = "max-age=63072000; includeSubDomains"

// ProductionHeadersConfig configures the production response-header posture —
// the Stripe/Cloudflare/GitHub-grade signals plus a security floor, stamped on
// every response (success, error, and 404) by ProductionHeaders.
type ProductionHeadersConfig struct {
	// Brand resolves a request Host to the white-label brand id emitted in the
	// Server response header ("hanzo", "lux", "zoo", ...). It is injected by the
	// service so this framework stays brand-agnostic: the brand registry has ONE
	// home in the service (e.g. cloud.BrandForHostOK). Return "" when the Host
	// matches no known brand — Neutral is emitted then. When Brand itself is nil,
	// every response uses Neutral.
	Brand func(host string) string

	// Neutral is the Server value used when Brand is nil or returns "". It MUST
	// be a brand id or a neutral platform token — NEVER a framework name
	// (fasthttp / fiber / zip), which would leak the stack. Defaults to "api".
	Neutral string

	// Version is emitted as the brand-neutral X-Api-Version header (the API
	// contract / build version, for support correlation). Empty omits the header.
	Version string

	// HSTS enables Strict-Transport-Security (max-age 63072000; includeSubDomains).
	HSTS bool
}

// ProductionHeaders stamps the production response-header posture on every
// response. Register it in the app.Use chain (mounted at "/") so it runs before
// the business handlers and its headers ride the response out on the success,
// error, AND 404 paths alike.
//
// It emits three orthogonal signals plus a security floor:
//
//   - Server                    white-label brand by Host (never the framework name)
//   - X-Api-Version             the API contract/build version (brand-neutral key)
//   - X-Content-Type-Options    nosniff (always)
//   - Strict-Transport-Security when HSTS is set
//
// X-Request-Id is owned by RequestID() — register it in the same chain; the two
// are orthogonal. ProductionHeaders never emits X-Powered-By or any
// framework/version string.
//
// The per-response Server set overrides the engine's default Server name on the
// wire (fasthttp routes Set("Server", …) through SetServer), so a lux request is
// never served "hanzo" and no response leaks "fasthttp"/"zip".
func ProductionHeaders(cfg ProductionHeadersConfig) zip.Handler {
	neutral := cfg.Neutral
	if neutral == "" {
		neutral = "api"
	}
	return func(c *zip.Ctx) error {
		server := neutral
		if cfg.Brand != nil {
			if b := cfg.Brand(c.Host()); b != "" {
				server = b
			}
		}
		c.SetHeader("Server", server)
		if cfg.Version != "" {
			c.SetHeader("X-Api-Version", cfg.Version)
		}
		c.SetHeader("X-Content-Type-Options", "nosniff")
		if cfg.HSTS {
			c.SetHeader("Strict-Transport-Security", hstsPolicy)
		}
		return c.Continue()
	}
}
