# zip — horizontal-scale audit

Branch: `feat/edge-scale-cleanup`. Coordinates with `feat/extract-auth-middleware` (auth.go already removed before this audit).

## Checklist

| Item | Status | Notes / fix |
|---|---|---|
| No global state | PASS | No package-level mutable vars. Confirmed by `grep -n "^var " *.go middleware/*.go` returning empty. |
| No package-level mutable maps | PASS | Same as above. |
| Middleware stack composable & pure | PASS | Each middleware in `middleware/` is a stateless constructor returning a `zip.Handler`. `RateLimit` keeps state in a closure tied to one constructor call (one limiter == one bucket table), not in a global. |
| `zip.App` is a value type carrying its own router | PASS | `App` (zip.go:98) holds its own `*fiber.App`. `New()` constructs a fresh fiber app per call. Verified by `TestTwoAppsNoStateBleed` (isolation_test.go). |
| No timestamps in handlers without an injectable clock | PASS | The only `time.Now()` calls live in middleware where they belong; `BreakerConfig.Now` is now injectable for tests. `RateLimit` does call `time.Now()` directly — acceptable for production but tests cannot fake time. Followup ticket would be to make `RateLimitConfig.Now` injectable too; intentionally not in this scope (RL is not on the gateway hot path). |
| Two `zip.App` instances in same process don't bleed state | PASS | New test `TestTwoAppsNoStateBleed` (isolation_test.go) registers disjoint routes on two Apps and asserts each App only serves its own. Also `TestTwoAppsIndependentShutdown` proves shutting down A leaves B serving. |
| Built on Fiber v3 | PASS | go.mod pins `github.com/gofiber/fiber/v3 v3.2.0`. `grep -rn "gofiber/fiber/v2"` returns empty. |
| No accidental cgo dependencies pulled in by middleware | PASS | Module compiles `CGO_ENABLED=0`. No `import "C"` anywhere. |

## What changed in this branch

1. `middleware/breaker.go` — new `Breaker` primitive (closed/open/half-open) + `zip.Handler` adapter. Per-process by design: N replicas each run their own breaker, no coordination, no shared state. Cross-replica coordination would be a bug. Default failure classifier treats non-nil handler errors and 5xx as failures; 4xx is the caller's fault and does not trip.
2. `middleware/breaker_test.go` — covers open after threshold, half-open transition, half-open re-open on failure, middleware happy path, middleware short-circuit, state-change callback, counter snapshot.
3. `isolation_test.go` — proves disjoint App instances do not leak routes or middleware to each other. This is the load-bearing horizontal-scale invariant for zip — every other check builds on it.

## Things deliberately NOT done in this branch

- `middleware/auth.go` removal — already done on `feat/extract-auth-middleware` (commit ffd591e). Do not duplicate.
- New `middleware/Auth()` re-export pointing at `hanzoai/gateway/middleware/` — that package does not yet exist as a public Go import path; gateway's auth lives at `hanzoai/gateway` root (auth_middleware.go). When the auth middleware is split into its own importable package, zip's middleware README's pointer is correct.
