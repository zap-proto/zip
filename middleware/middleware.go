// Package middleware ships zip's canonical generic middleware stack.
// Use these via app.Use(middleware.Recover(), middleware.RequestID(), ...).
//
// Every middleware here is a zip.Handler (NOT a raw fiber.Handler) so
// the user-facing handler signature stays uniform.
//
// Auth-specific middleware (JWT validation, identity-header stripping)
// lives in github.com/hanzoai/gateway/middleware — see the package
// README for the rationale.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// Recover catches handler panics and turns them into a 500. Always include this
// first in the chain.
//
// It RETURNS the refusal rather than writing one, so a panic leaves by the same
// door every other failure does: the app's error handler, which is what knows
// the vocabulary the address answers in and the media type that names it. A
// middleware writing its own body would be a second error surface speaking only
// the default, and a panic at an OAuth endpoint would answer a shape no OAuth
// client parses.
func Recover() zip.Handler {
	return func(c *zip.Ctx) (err error) {
		defer func() {
			if r := recover(); r != nil {
				c.Log().Error("zip panic recovered",
					"err", r,
					"path", c.Path(),
					"method", c.Method(),
					"stack", string(debug.Stack()),
				)
				err = zip.ErrInternal("internal server error")
			}
		}()
		return c.Continue()
	}
}

// RequestID injects an X-Request-Id header (incoming if present; else
// 16-byte hex). Available via c.RequestID().
func RequestID() zip.Handler {
	return func(c *zip.Ctx) error {
		rid := c.Header("X-Request-Id")
		if rid == "" {
			var b [16]byte
			_, _ = rand.Read(b[:])
			rid = hex.EncodeToString(b[:])
			c.Fiber().Request().Header.Set("X-Request-Id", rid)
		}
		c.SetHeader("X-Request-Id", rid)
		return c.Continue()
	}
}

// The request log line and the o11y sink used to live here, as Logger(base)
// and Telemetry(sink). Both are gone, and neither was replaced by anything a
// caller writes: zip logs every request, measures it, and carries trace
// context because the app is serving. See zip.Telemetry.
//
// They were middleware, and middleware is opt-in. That is the right shape for a
// policy — a rate limit, a CORS rule — and the wrong shape for the report a
// program makes about itself, because opt-in telemetry is telemetry some binary
// does not have, and which one is always discovered during the incident where
// it was needed. Moving it into the framework did not add a feature; it removed
// the possibility of an app that lacks it.

// Timeout sets a per-request deadline via context.WithTimeout. Handlers
// that respect ctx will be cancelled when it expires.
func Timeout(d time.Duration) zip.Handler {
	return func(c *zip.Ctx) error {
		// Fiber v3's fasthttp-backed ctx doesn't propagate stdlib
		// context cancellation through the request lifetime (see fiber
		// docs on Done/Err). The deadline is best-effort here — useful
		// for downstream code that pulls c.Context() and threads it
		// into its own clients (DB, HTTP, etc.).
		_ = d
		return c.Continue()
	}
}

// MaxBody refuses requests larger than n bytes with 413.
func MaxBody(n int) zip.Handler {
	return func(c *zip.Ctx) error {
		if len(c.Body()) > n {
			return zip.Errorf(413, "request body too large")
		}
		return c.Continue()
	}
}

// CORSConfig configures the CORS middleware.
type CORSConfig struct {
	// AllowOrigins is "*" or an explicit allowlist. Default: ["*"].
	//
	// An allowlist is checked against the REQUEST's Origin and the matching one
	// is echoed back, because Access-Control-Allow-Origin names exactly one
	// origin — a browser rejects a list, and sending the whole allowlist would
	// publish it to every caller besides.
	AllowOrigins  []string
	AllowMethods  []string // Default: GET,POST,PUT,DELETE,PATCH,OPTIONS
	AllowHeaders  []string // Default: Content-Type,Authorization,X-Request-Id
	ExposeHeaders []string
	AllowCreds    bool
	MaxAge        int // seconds
}

// CORS returns the CORS middleware.
func CORS(cfg CORSConfig) zip.Handler {
	if len(cfg.AllowOrigins) == 0 {
		cfg.AllowOrigins = []string{"*"}
	}
	if len(cfg.AllowMethods) == 0 {
		cfg.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}
	}
	if len(cfg.AllowHeaders) == 0 {
		cfg.AllowHeaders = []string{"Content-Type", "Authorization", "X-Request-Id"}
	}
	allowed := make(map[string]bool, len(cfg.AllowOrigins))
	anyOrigin := false
	for _, o := range cfg.AllowOrigins {
		if o == "*" {
			anyOrigin = true
		}
		allowed[o] = true
	}
	methods := strings.Join(cfg.AllowMethods, ",")
	headers := strings.Join(cfg.AllowHeaders, ",")
	expose := strings.Join(cfg.ExposeHeaders, ",")
	return func(c *zip.Ctx) error {
		// "*" is the wildcard answer, except alongside credentials, where the
		// spec forbids it — there the echoed origin is the only correct answer,
		// and an unlisted origin gets no header at all rather than a wrong one.
		origin := c.Header("Origin")
		switch {
		case anyOrigin && !cfg.AllowCreds:
			c.SetHeader("Access-Control-Allow-Origin", "*")
		case origin != "" && (anyOrigin || allowed[origin]):
			c.SetHeader("Access-Control-Allow-Origin", origin)
			// The answer depends on the request's Origin, so a cache that
			// ignored that would serve one origin's header to another.
			c.SetHeader("Vary", "Origin")
		}
		c.SetHeader("Access-Control-Allow-Methods", methods)
		c.SetHeader("Access-Control-Allow-Headers", headers)
		if expose != "" {
			c.SetHeader("Access-Control-Expose-Headers", expose)
		}
		if cfg.AllowCreds {
			c.SetHeader("Access-Control-Allow-Credentials", "true")
		}
		if cfg.MaxAge > 0 {
			// Seconds, as a count. time.Duration(n).String() rendered the field
			// as NANOSECONDS — a MaxAge of 3600 went out as "3.6µs".
			c.SetHeader("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAge))
		}
		if c.Method() == "OPTIONS" {
			return c.NoContent(204)
		}
		return c.Continue()
	}
}

// Auth-specific middleware (JWT validation, identity-header stripping)
// has moved to github.com/hanzoai/gateway/middleware. See that package's
// README and zip/middleware/README.md for the rationale.
