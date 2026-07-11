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
the 24 ns wrapper is **<1%**. The one allocation is the price of the clean
`*zip.Ctx` surface; eliminating it would mean pooling ctx values (fiber already
pools its own) — a possible future optimisation, not a correctness issue.

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
