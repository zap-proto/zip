# Benchmarks — zip (ZAP-native web framework)

zip wraps the [`zap-proto/fiber`](https://github.com/zap-proto/fiber) v3 fork.
This file measures **what zip costs on top of raw fiber** — the framework tax —
against hand-written baselines, so every number is the *overhead*, not the
absolute. Routing performance itself is inherited from fiber and is documented
in that repo's [`BENCHMARKS.md`](https://github.com/zap-proto/fiber/blob/main/BENCHMARKS.md)
(headline: 0-alloc matching; fork adds zero aggregate match cost vs upstream).

Numbers are from committed benchmarks ([`bench_test.go`](bench_test.go)); the
JSON edge path also has [`json_bench_test.go`](json_bench_test.go).

## Environment

| | |
|---|---|
| CPU | Apple M1 Max (10 core) |
| RAM | 64 GB |
| OS | Darwin 25.5.0, arm64 (macOS) |
| Go | `go1.26.4 darwin/arm64` |
| Method | `-benchmem -benchtime=1s -count=6`, median via `benchstat` (`golang.org/x/perf`) |

`±` is `benchstat`'s across-run variation. Laptop host: treat `sec/op`
differences under ~5% as noise; `allocs/op` is exact.

§1a's numbers are from a second host — 20-core arm64 Linux (`go1.26.5`), and a
**busy** one: unrelated builds held it at load 30–140 throughout, and the same
benchmark varied up to 3.3× between samples. Wall-clock figures there are
therefore reported as the **minimum of 24 samples taken alternately** from the
before and after binaries, so drifting interference hits both equally and the
minimum is the sample least polluted by it. Treat them as indicative of
direction and rough magnitude, not as calibrated absolutes. The `allocs/op` and
`B/op` columns carry no such caveat — they are exact counts, identical on every
run and every host.

## 1. Per-request wrapper tax — `zip.App` vs raw `fiber.App`

zip routes through `toFiberHandler`, which materialises a `*Ctx{fc, app, log}`
per request. To isolate exactly that, the handlers are byte-equivalent: zip's
`c.NoContent(204)` is `c.fc.Status(204); return nil`, and the fiber baseline is
`c.Status(204); return nil`. Same underlying work, only difference is the
wrapper. Dispatched via `app.Fiber().Handler()` on a reused `fasthttp.RequestCtx`.

| route | zip `ns/op` | raw fiber `ns/op` | tax | zip `allocs` | fiber `allocs` |
|---|--:|--:|--:|--:|--:|
| static (`/v1/health`) | 80.5 | 56.1 | **+24.4 ns** | 1 (48 B) | 0 |
| param (`/v1/tracker/:id`) | 91.3 | 67.1 | **+24.3 ns** | 1 (48 B) | 0 |

**The tax is a constant ~24 ns and exactly one 48-byte allocation** (the `&Ctx`),
independent of routing shape. On a no-op handler that is +43%; on any real
handler it is noise — the next section's typed handler does ~3,500 ns of work, so
the 24 ns wrapper is **<1%**.

### 1a. …and independent of chain depth (`Benchmark_ChainTax`)

Routing shape is not the only axis a deployed service moves on: every service
runs a `Use` stack above its leaf. `toFiberHandler` wraps *each* zip handler, so
the wrapper's cost used to grow linearly with the chain — a 5-middleware stack
allocated 6 `*Ctx` per request, not 1.

It no longer does. One request gets ONE `*Ctx`, created on first touch and kept
in the request's own user-value storage, so every handler in the chain is handed
the same value. Measured with `allocs/op`, which is exact and host-independent:

| `Use` middleware | allocs/op before | allocs/op now | B/op before | B/op now |
|---|--:|--:|--:|--:|
| 0 | 1 | 1 | 48 | 48 |
| 1 | 2 | **1** | 96 | **48** |
| 3 | 4 | **1** | 192 | **48** |
| 5 | 6 | **1** | 288 | **48** |

On a contended 20-core arm64 host (min of 24 alternating samples — see
"Environment" caveat below) that was **−48.9%** wall clock at depth 5
(280.8 → 143.6 ns/op) and **+3.3%** at depth 0 (76.4 → 78.9 ns/op): a bare
single-handler route pays ~2.5 ns to store the value it will not reuse, and
every route with middleware — i.e. every real one — wins several times that.

`TestServePathAllocsAreChainDepthInvariant` pins the property so it cannot
regress silently, and `TestSetLogReachesDownstream` pins its correctness
half: because the chain shares one `*Ctx`, a `c.SetLog()` in middleware is
now actually visible to the handlers below it, which is what
`middleware.Logger` has always meant by attaching request_id / org / user.

The remaining one allocation per request is the price of the clean `*zip.Ctx`
surface. Removing it would mean pooling ctx values across requests, which
buys 48 B at the cost of a use-after-free footgun — not worth it.

> **Measuring this correctly:** these benchmarks reuse one
> `fasthttp.RequestCtx`, so each iteration must clear the previous request's
> user values (`fctx.ResetUserValues()`) exactly as both transports do before
> dispatching — `zap-proto/http`'s `serveConn`, and fasthttp's keep-alive loop
> via `Request.Reset`. Omit it and an iteration inherits the last one's
> request-scoped `*Ctx`: the wrapper then reports **0 allocs/op** for a path
> that really allocates one per request.

## 2. Typed routes — `Get/Post[In,Out]` vs hand-written

The generic `Post[In,Out]` decodes the JSON body → `In`, validates, runs the
handler, and encodes `Out` → JSON, through the transport-agnostic `op.invoke`
core that also feeds the OpenAPI and MCP projections. Compared against an
idiomatic hand-written zip handler doing `c.Bind` + `c.JSON` — the same work
without generics. Identical `chatRequest`/`chatResponse` payload (shared with
`json_bench_test.go`). Median of 6.

| implementation | `ns/op` | `B/op` | `allocs/op` |
|---|--:|--:|--:|
| **typed `Post[In,Out]`** | **3,507** | **1,220** | **27** |
| hand-written `c.Bind`+`c.JSON` | 3,581 | 1,268 | 28 |

**The generic sugar is free — in fact marginally leaner** (−2% time, −1 alloc).
`Post[In,Out]` calls the JSON decoder directly on `c.Body()`, while the
hand-written `c.Bind()` goes through fiber's content-type–sniffing bind
machinery, which is where the extra allocation comes from. So the ergonomic,
OpenAPI/MCP-generating typed API costs **nothing** versus writing the
decode/encode by hand. The ~3.5 µs is dominated by JSON marshal/unmarshal of the
chat payload (25+ of the 27 allocs), not by zip.

## Verdict

| claim | verdict |
|---|---|
| zip's per-request overhead over fiber | **~24 ns + 1 alloc (48 B)**, constant; <1% of real handler cost (§1) |
| …and that 1 alloc holds however deep the middleware chain is | **True** — one `*Ctx` per request, not per handler (§1a) |
| Typed `Get/Post[In,Out]` costs extra vs hand-written | **False** — on par / marginally leaner (§2) |
| Routing performance | Inherited from `zap-proto/fiber` — see its BENCHMARKS.md (0-alloc match) |

## Reproduce

```sh
go test -run='^$' -bench='Benchmark_ZipTax|Benchmark_TypedRoute' -benchmem -count=6 .

# JSON edge path (v1 vs json/v2)
go test -run='^$' -bench='BenchmarkJSON' -benchmem -count=6 .
GOEXPERIMENT=jsonv2 go test -run='^$' -bench='BenchmarkJSON' -benchmem -count=6 .

# Medians (requires golang.org/x/perf/cmd/benchstat)
go test -run='^$' -bench='Benchmark_ZipTax|Benchmark_TypedRoute' -benchmem -count=6 . | benchstat -
```
