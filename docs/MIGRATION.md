# Migration guide

zip ships drop-in adapters for chi, gin, beego, and stdlib `net/http`.
Each adapter is documented as a **migration tool** — they exist so a
service can roll onto zip incrementally. Adapter dispatch costs ~5%
versus native Fiber routing; replace adapted routes with native zip
handlers when feasible.

## From gin

gin handlers take `*gin.Context`; zip handlers take `*zip.Ctx`. The
shapes are deliberately close.

```go
// gin
r := gin.Default()
r.GET("/users/:id", func(c *gin.Context) {
    c.JSON(200, gin.H{"id": c.Param("id")})
})

// zip
app := zip.New(zip.Config{})
app.Get("/users/:id", func(c *zip.Ctx) error {
    return c.JSON(200, map[string]string{"id": c.Param("id")})
})
```

Common mappings:

| gin | zip |
|---|---|
| `gin.H{...}` | `map[string]any{...}` or a typed struct |
| `c.Param("id")` | `c.Param("id")` |
| `c.Query("q")` | `c.Query("q")` |
| `c.GetHeader("X-Foo")` | `c.Header("X-Foo")` |
| `c.BindJSON(&v)` | `c.Bind(&v)` |
| `c.AbortWithStatusJSON(400, ...)` | `return zip.ErrBadRequest("...")` |
| `gin.Recovery()` | `middleware.Recover()` |
| `gin.Logger()` | `middleware.Logger(app.Logger())` |

For routes that are too complex to port today, mount the gin engine as
a legacy adapter:

```go
gin := buildExistingGinApp()
app.Mount("/legacy/gin", gin)
```

## From chi

chi routers implement `http.Handler`, so `zip.AdaptNetHTTP` works
unchanged.

```go
chiRouter := chi.NewRouter()
chiRouter.Get("/users", listUsers)

app := zip.New(zip.Config{})
app.Mount("/legacy/chi", chiRouter)
```

Common mappings:

| chi | zip |
|---|---|
| `chi.NewRouter()` | `zip.New(zip.Config{})` |
| `r.Route("/v1", fn)` | `app.Route("/v1", fn)` |
| `r.Group(fn)` | `app.Group("/", h1, h2)` |
| `chi.URLParam(r, "id")` | `c.Param("id")` |
| middleware (func(http.Handler) http.Handler) | `middleware.AdaptNetHTTPMiddleware(mw)` |

## From beego

beego `*web.HttpServer` exposes its handler chain via `BeeApp.Handlers`,
which is an `http.Handler`. Mount it via `app.Mount`.

```go
import "github.com/beego/beego/v2/server/web"

beeApp := web.NewHttpServer()
// ... existing beego config

app := zip.New(zip.Config{})
app.Mount("/legacy/iam", beeApp.Handlers)
```

Notes:

- beego's auto-routing magic (controllers reflected from struct names)
  has no zip equivalent — port to native zip handlers when feasible.
- beego's `app.conf` and beego-specific filters do not carry over;
  re-implement filters as zip middleware.

## From net/http

The zero-config path: any `http.Handler` works.

```go
existing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.Write([]byte("hello"))
})
app.Mount("/legacy", existing)
```

For middleware that takes `func(http.Handler) http.Handler`:

```go
mw := func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Custom", "1")
        next.ServeHTTP(w, r)
    })
}
app.Use(zip.AdaptNetHTTPMiddleware(mw))
```

## Replacing adapters with native handlers

Once a route's traffic warrants it, rewrite from `Mount` to native zip:

```go
// before
app.Mount("/legacy/chi", chiRouter)

// after (per-route migration)
v1 := app.Group("/v1")
v1.Get("/users", listUsers)        // was chiRouter.Get("/users", ...)
v1.Post("/users", createUser)
```

The ~5% perf delta compounds at high RPS and the adapter chain is a
sharp edge for debugging — native handlers see fiber.Ctx directly,
adapters bounce through fasthttp ↔ net/http translation.
