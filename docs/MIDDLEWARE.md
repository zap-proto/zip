# Middleware reference

All zip middleware is a `zip.Handler` (not raw `fiber.Handler`) so the
user-facing handler signature stays uniform. Install via `app.Use(...)`.

```go
import "github.com/hanzoai/zip/middleware"

app.Use(
    middleware.Recover(),
    middleware.RequestID(),
    middleware.Logger(app.Logger()),
)
```

## Recover

Catches handler panics, logs the stack via luxfi/log, and returns a 500
JSON response. Always include first.

```go
app.Use(middleware.Recover())
```

## RequestID

Injects `X-Request-Id`. If the incoming request carries one, it's
preserved; otherwise zip mints a 16-byte hex value. Available via
`c.RequestID()`.

```go
app.Use(middleware.RequestID())
```

## Logger

Wraps each request with a per-request luxfi/log child logger that
includes `request_id`, `org`, `user`. Logs the request line at
completion with `status` + `dur_ms`.

```go
app.Use(middleware.Logger(app.Logger()))
```

## Timeout

Per-request context deadline. Best-effort — Fiber's fasthttp-backed Ctx
does not natively propagate context cancellation through the request
lifetime; downstream code that consumes `c.Context()` (DB calls, HTTP
clients) gets the deadline.

```go
app.Use(middleware.Timeout(30 * time.Second))
```

## MaxBody

Rejects request bodies larger than `n` bytes with 413.

```go
app.Use(middleware.MaxBody(1 << 20))  // 1 MiB
```

## CORS

```go
app.Use(middleware.CORS(middleware.CORSConfig{
    AllowOrigins:  []string{"https://app.hanzo.ai"},
    AllowMethods:  []string{"GET", "POST"},
    AllowHeaders:  []string{"Content-Type", "Authorization"},
    AllowCreds:    true,
    MaxAge:        86400,
}))
```

## Auth & StripIdentityHeaders — moved

Auth-specific middleware (JWT validation, identity-header stripping)
has moved to
[`github.com/hanzoai/gateway/middleware`](https://github.com/hanzoai/gateway/tree/main/middleware).

Rationale: JWT validation + identity-header minting are the gateway
subsystem's responsibility per HIP-0106. Other subsystems mounted
inside the unified `cloud` binary trust the gateway-minted `X-Org-Id`
header and do not re-validate JWTs themselves. The trust-assertion
helper `gateway.AssertGatewayMinted(c)` lets a downstream handler
defend against deployment misconfiguration where it is accidentally
exposed to direct (non-gateway) traffic.

## RateLimit

Per-org (or per-IP fallback) in-memory token bucket. Single-pod only —
multi-pod deployments must rate-limit at the gateway.

```go
app.Use(middleware.RateLimit(middleware.RateLimitConfig{
    Limit:  100,
    Window: time.Minute,
}))
```

## Telemetry

Plumbs request metrics to any `O11ySink` implementation. nil sink is a
no-op.

```go
app.Use(middleware.Telemetry(myO11ySink))
```

## Order

Recommended order for a Hanzo service behind hanzoai/gateway:

```go
app.Use(
    middleware.Recover(),              // 1. always first
    middleware.RequestID(),            // 2. mint request id
    middleware.Logger(app.Logger()),   // 3. log with request id
    middleware.Telemetry(o11y),        // 4. metrics
    middleware.RateLimit(rlCfg),       // 5. limit
    middleware.CORS(corsCfg),          // 6. last — close to response
)
```

JWT validation + identity-header stripping are owned by the gateway
subsystem; see
[`github.com/hanzoai/gateway/middleware`](https://github.com/hanzoai/gateway/tree/main/middleware).
