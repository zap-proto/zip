# zip

zip is a Go web framework on fasthttp. You write an operation once, as a function with a typed input and output, and zip serves it as a REST route, an OpenAPI operation, an MCP tool, a CLI command and a call other services make over ZAP.

## Install

```sh
go get github.com/zap-proto/zip@latest
go list -m github.com/zap-proto/zip
```

```
github.com/zap-proto/zip v1.36.48
```

zip needs Go 1.26.8 or newer. `go list -m -versions github.com/zap-proto/zip` lists every release. A copy taken from an old module cache may be many releases behind; see [Upgrading](#upgrading).

## A service in one file

[`examples/local-service`](examples/local-service) stores notes in SQLite and has one operation. This is its `main.go`:

```go
// Command local-service is a zip service with one operation, backed by SQLite.
//
// The operation is declared once, in New, and every interface below is derived
// from that declaration:
//
//	go run . serve                          # REST, MCP and OpenAPI on 127.0.0.1:8080
//	go run . notes create --text "buy milk" # the same operation as a command
//	go run . openapi openapi.json           # the OpenAPI document, written to a file
//
// Notes are stored in notes.db in the working directory.
package main

//go:generate go tool zipdoc

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/hanzoai/sqlite"
	"github.com/zap-proto/zip"
)

// Note is one stored note.
type Note struct {
	// ID is the row id SQLite assigned.
	ID int64 `json:"id"`
	// Text is the note as written.
	Text string `json:"text"`
}

// AddIn is a note to store.
type AddIn struct {
	// Text is the note body.
	Text string `json:"text" validate:"required"`
}

// Store keeps notes in one SQLite file.
type Store struct {
	db *sql.DB
}

// Open opens the SQLite file at path, creating it and its table if needed.
func (s *Store) Open(path string) error {
	db, err := sqlite.OpenDB(path, nil)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, text TEXT NOT NULL)`); err != nil {
		db.Close()
		return err
	}
	s.db = db
	return nil
}

// Add stores a note and returns it with its id.
//
// Example: {"text": "buy milk"}
// Response: {"id": 1, "text": "buy milk"}
func (s *Store) Add(ctx context.Context, in *AddIn) (*Note, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO notes (text) VALUES (?)`, in.Text)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Note{ID: id, Text: in.Text}, nil
}

// New declares the service's operations. It does not touch the database, so the
// OpenAPI document can be written by a build step that has none.
func New(s *Store) *zip.App {
	app := zip.New(zip.Config{AppName: "notes"})
	zip.Post(app, "/v1/notes", s.Add)
	return app
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	s := &Store{}
	app := New(s)
	// `local-service openapi <file>` and `local-service declare <file>` write a
	// projection and stop here, before the database is opened.
	if done, err := app.Described(); done {
		return err
	}
	if err := s.Open("notes.db"); err != nil {
		return err
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		return app.Listen("http://127.0.0.1:8080")
	}
	cli := app.CLI()
	cli.Out = os.Stdout
	return cli.Run(context.Background(), os.Args[1:])
}
```

Start it:

```sh
cd examples/local-service
go run . serve
```

From a second terminal, call the operation three ways. Over REST:

```sh
curl -s -X POST http://127.0.0.1:8080/v1/notes -H 'Content-Type: application/json' -d '{"text":"buy milk"}'
```

```json
{"id":1,"text":"buy milk"}
```

As an MCP tool:

```sh
curl -s -X POST http://127.0.0.1:8080/mcp -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"post_notes","arguments":{"text":"call the bank"}}}'
```

```json
{"jsonrpc":"2.0","id":3,"result":{"content":[{"text":"{\"id\":2,\"text\":\"call the bank\"}","type":"text"}]}}
```

As a command. It runs the handler in this process, against the same `notes.db`; zip's log lines go to stderr:

```sh
go run . notes create --text "water the plants"
```

```json
{
  "id": 3,
  "text": "water the plants"
}
```

One handler and one table produced ids 1, 2 and 3. The OpenAPI document needs neither a server nor the database:

```sh
go run . openapi openapi.json
```

The `/v1/notes` entry, with its examples removed:

```json
{
  "post": {
    "operationId": "post_notes",
    "summary": "Stores a note and returns it with its id.",
    "description": "Stores a note and returns it with its id.",
    "requestBody": {
      "required": true,
      "content": {"application/json": {"schema": {"$ref": "#/components/schemas/AddIn"}}}
    },
    "responses": {
      "200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Note"}}}},
      "default": {"description": "refused", "content": {"application/problem+json": {"schema": {"$ref": "#/components/schemas/problem-details"}}}}
    }
  }
}
```

`go generate` rebuilds `zipdoc_gen.go` from the doc comments, and `go test` checks the operation over REST, MCP, OpenAPI and the CLI. The directory is its own module, so the SQLite driver (`github.com/hanzoai/sqlite`) is not a dependency of zip. A copy outside this repository drops the `replace` line from its `go.mod`.

## Projections

Everything below starts from the one declaration `zip.Post(app, "/v1/notes", s.Add)`.

### REST

| | |
|---|---|
| Declare | `zip.Get`, `zip.Post`, `zip.Put`, `zip.Patch`, `zip.Delete`, called as `zip.Post[In, Out](on, path, fn, opts...)`. `on` is the `*zip.App`, `app.Group("/v1")` or `app.With(middleware...)`. |
| Input | The JSON body (GET, HEAD and DELETE have none), then fields tagged `header:"X-Name"`, then the query string, then path parameters. The URL wins. `validate:"required"` refuses a request without the field. |
| Options | `zip.WithOperationID`, `zip.WithSummary`, `zip.WithTags`, `zip.WithStatus(201)`, `zip.WithResponseHeader`. |
| Untyped routes | `app.Get(path, func(c *zip.Ctx) error)` registers a route with no operation, so it is in none of the projections below. Use it for streams, protocol upgrades and non-JSON bodies. |
| Tests | `app.Test(httptest.NewRequest(...))` serves a request without a socket, including `/mcp` and the OpenAPI document. |

### OpenAPI

- `GET /.well-known/openapi.json` (`zip.SpecPath`) serves OpenAPI 3.1. `app.OpenAPISpec()` returns the same document as a map.
- `app.Described()` writes a projection when the binary runs as `<binary> openapi <file>` or `<binary> declare <file>`, and reports whether it did. Call it after registering operations and before opening a database, as `run` does above.
- `zip.Config{OpenAPI: zip.OpenAPIConfig{Title, Description, Version, Disabled}}` sets the `info` block or turns the document off.
- Every operation declares a `default` response: the `problem-details` schema under `application/problem+json`, or `oauth-error` at an address declared with `zip.OAuth`. SDK generators type errors from it.
- `GET /docs` (`zip.DocsPath`) is a Swagger UI page that loads its script and stylesheet from cdn.jsdelivr.net, so it is blank without network access. The JSON document needs none.

### Doc comments

`cmd/zipdoc` copies doc comments into the OpenAPI document, the MCP tool list and the CLI help:

```sh
go get -tool github.com/zap-proto/zip/cmd/zipdoc
```

Put `//go:generate go tool zipdoc` in the package that registers the operations and run `go generate`. It writes `zipdoc_gen.go`: the handler's comment becomes the description, its `Example:` and `Response:` lines become examples, and field comments become schema descriptions. In CI, run `go tool zipdoc -check` in that package's directory. It fails when `zipdoc_gen.go` no longer matches the source.

### MCP

Each operation is a tool named by its operation id (`post_notes`), with its input schema as `inputSchema`. GET operations carry `readOnlyHint: true`. `app.MCPTools()` returns the list in-process.

```sh
curl -s -X POST http://127.0.0.1:8080/mcp -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'
```

```json
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{"listChanged":false}},"protocolVersion":"2025-11-25","serverInfo":{"name":"notes","version":""}}}
```

What the HTTP door does:

| | |
|---|---|
| Transport | `POST /mcp`: one JSON-RPC message per request, answered with one `application/json` message. |
| GET and DELETE | `405 Method Not Allowed` with `Allow: POST`. There is no SSE stream; a request that accepts only `text/event-stream` still gets JSON. |
| Sessions | None. No `Mcp-Session-Id`, and nothing is kept between requests. |
| Batches | Not accepted. A JSON array answers `-32700 parse error`. |
| Notifications | `202 Accepted`. |
| Methods | `initialize`, `server/discover`, `ping`, `tools/list`, `tools/call`. No resources, prompts, sampling or logging. |
| Protocol version | `initialize` answers the revision the client asked for when it is 2025-06-18, 2025-11-25 or 2026-07-28, and 2026-07-28 otherwise. |
| Tool errors | A result with `isError: true` and the error text, not a JSON-RPC error. |
| Origin | Not checked. Bind a local service to 127.0.0.1, as the example does, and serve anything else behind a gateway. |

[`examples/local-service/.mcp.json`](examples/local-service/.mcp.json) registers the running example with Claude Code. The official MCP clients connect to it, list its tools and call them: the TypeScript SDK 1.30.0, and the Python SDK 2.2.0 through `initialize` and in its default `auto` mode.

`zip.Config{MCP: zip.MCPConfig{...}}` has `Disabled`, `Path` and `Addr`, which serves the same tools over ZAP with no HTTP listener. `app.MCP` is the door itself, a `zapmcp.Handler`.

### CLI

`app.CLI()` returns a `*zip.CLI` that runs operations in this process. Set `Out` before `Run`:

```go
cli := app.CLI()
cli.Out = os.Stdout
err := cli.Run(ctx, os.Args[1:])
```

A command is `<service> <operation>`: the first path segment after the version, then a verb from the method, so `POST /v1/notes` is `notes create`. Path parameters are arguments and the other input fields are flags. `--help` prints the doc comment, the flags and an example:

```
$ go run . notes create --help
notes notes create [flags]

Stores a note and returns it with its id.

POST /v1/notes

Flags:
  --text string  Text is the note body. (required)

Example:
  notes notes create --text "buy milk"
```

A client that links none of the service builds the same commands from the served document with `zip.CommandsFromSpec`, and sends them with `zip.Remote`:

```go
remote := zip.Remote{Base: "http://127.0.0.1:8080"}
spec, err := remote.Spec(ctx)
if err != nil {
	return err
}
commands, err := zip.CommandsFromSpec(spec)
if err != nil {
	return err
}
cli := &zip.CLI{Name: "notes", Commands: commands, Invoke: remote.Invoke, Out: os.Stdout}
return cli.Run(ctx, os.Args[1:])
```

```
$ go run . notes create --text "sent over HTTP"
{
  "id": 4,
  "text": "sent over HTTP"
}
```

`Remote.Header` sets headers, such as `Authorization`, on every request.

### Calls between services

```go
conn, err := zip.Dial("http://127.0.0.1:8080")
if err != nil {
	return err
}
note, err := zip.Call[AddIn, Note](ctx, conn, "post_notes", &AddIn{Text: "over the call plane"})
```

The caller imports or restates `AddIn` and `Note` and links nothing else of the service. A refusal is a `*zip.HTTPError` carrying the callee's status. `zip.DialApp(name)` dials the unix socket at `zip.SocketPath(name)`, which is how services on one host reach each other.

This body is ZAP, laid out in the order of the struct's fields. **Add fields only at the end of an input or output struct.** Reordering, inserting or retyping a field changes the wire and breaks callers built against the old layout. REST and MCP bodies are JSON, where field order does not matter.

### SDKs

| Target | How |
|---|---|
| Go | `app.SDK("notes")` returns a `*zip.SDK`. `Source` is a package whose `Client` (`notes.Dial(addr)`, then `client.PostNotes(ctx, in)`) uses `zip.Call`; `Gaps` lists operations it could not express. |
| Rust, C++ | `app.RustSDK(crate)`, `app.CppSDK(namespace)`. |
| From a schema | `cmd/zipgen` reads a `.zap` schema, not Go code. `zip.ZAPSchema("notes", app).String()` writes one; then `go run github.com/zap-proto/zip/cmd/zipgen sdk -schema notes.zap -lang go -pkg notes -o client.go`. `-lang` also takes `rust` and `cpp`, and the other subcommands are `mcp`, `cli`, `docs` and `zap`. |
| Other languages | Generate from `/.well-known/openapi.json`. |

### Errors

Every refusal is an RFC 9457 problem document under `application/problem+json`:

```sh
curl -s -X POST http://127.0.0.1:8080/v1/notes -H 'Content-Type: application/json' -d '{}'
```

```json
{"detail":"field \"text\" is required","status":400,"title":"Bad Request","type":"about:blank"}
```

- A handler returns `zip.ErrBadRequest(msg)`, `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`, `ErrConflict`, `ErrPaymentRequired`, `ErrUnprocessable`, `ErrInternal`, or `zip.Errorf(status, format, args...)`. `&zip.HTTPError{Status: 409, Code: "taken", Msg: "..."}` adds `code`, and `.With(map[string]any{"cap": 5000})` adds members beside the standard ones.
- Any other error is a 500 whose `detail` is `err.Error()`. Wrap errors whose text a caller should not read.
- An address declared through `zip.OAuth(router)` answers RFC 6749 `{"error", "error_description"}` under `application/json` instead.
- `zip.Config{ErrorHandler: ...}`, a `fiber.ErrorHandler` from `github.com/zap-proto/fiber/v3`, replaces the writer for every refusal.
- Over MCP a refusal is `isError` content; `CLI.Run` returns it as an error.

Problem documents arrived in v1.32.0. Through v1.31.4, v1.18.x included, a refusal was `{"status":400,"code":"...","error":"..."}` under `application/json`; code that reads `error` reads `detail` now.

### Security

zip validates no tokens. It reads identity from headers set by the gateway in front of it, after the gateway has checked the Hanzo IAM token: `X-Org-Id`, `X-User-Id`, `X-User-Email` and the others named by the `zip.Header*` constants. `zip.CallerOf(ctx)` returns them in a handler. A service reachable without that gateway in front must not trust these headers.

`app.Authorize` installs one rule that runs for every operation after its input is decoded and validated and before the handler, however the operation was reached: REST, MCP, `zip.Call` or the in-process CLI.

```go
app.Authorize(func(ctx context.Context, op zip.Op, in any) (zip.Decision, error) {
	if zip.CallerOf(ctx).Org == "" {
		return zip.Decision{Effect: zip.Deny, Clause: "org", Reason: "no org on the request"}, nil
	}
	return zip.Decision{Effect: zip.Allow}, nil
})
```

A request to the example without `X-Org-Id` is then refused with a 403:

```json
{"code":"org","detail":"no org on the request","status":403,"title":"Forbidden","type":"about:blank"}
```

- `zip.Deny` is a 403 with `Clause` as `code` and `Reason` as `detail`. `zip.Approve` holds the operation and answers 202 with a `zip.Approval`; a Go caller reads it with `zip.HeldOf(err)`. A returned error aborts the call.
- The in-process CLI has no request, so `CallerOf` is empty unless the context was built with `zip.WithCaller(ctx, zip.Caller{Org: "acme"})`.
- `app.OnResult(fn)` is told the outcome of every operation, for audit records.
- `CallerOf(ctx).IP` is the socket peer. `Config.TrustProxy`, `TrustedProxies` and `ProxyHeader` opt in to an address a trusted proxy forwards.

## Testing

`TestEveryOperationIsInEveryProjection` in [`examples/local-service/main_test.go`](examples/local-service/main_test.go) reads the router and checks each operation against the OpenAPI document, the MCP tool list and the CLI, so an operation added later is covered with no change to the test:

```go
for _, r := range app.Routes() {
	if r.Op == "" {
		continue // an untyped route has no projections
	}
	op := spec.Paths[zip.Template(r.Pattern)][strings.ToLower(r.Method)]
	if op.OperationID != r.Op {
		t.Errorf("%s %s: OpenAPI operationId %q, want %q", r.Method, r.Pattern, op.OperationID, r.Op)
	}
	if !tools[r.Op] {
		t.Errorf("%s %s: no MCP tool %q", r.Method, r.Pattern, r.Op)
	}
	if !commands[r.Op] {
		t.Errorf("%s %s: no CLI command %q", r.Method, r.Pattern, r.Op)
	}
}
```

## Upgrading

| Version | Change |
|---|---|
| v1.32.0 | Refusals are RFC 9457 problem documents under `application/problem+json`. Before, `{"status","code","error"}` under `application/json`. |
| v1.36.48 | The OpenAPI document declares a `default` refusal response on every operation. MCP `initialize` answers the revision the client asked for. Doc comments reach binaries whose handlers are in package main; before, they did only under `go test`. |

v1.36.21 is retracted.

## Repository

| Path | |
|---|---|
| `examples/local-service` | the service above, a module of its own |
| `examples/hello`, `examples/zap-typed` | smaller typed services |
| `examples/sse-streaming`, `examples/websocket` | the two untyped routes, each saying why |
| `examples/migrate-from-chi`, `-gin`, `-beego` | existing apps moved onto zip |
| `cmd/zipdoc`, `cmd/zipgen` | doc comments; code generation from `.zap` schemas |
| `middleware` | `Recover`, `RequestID`, `Timeout`, `MaxBody`, `CORS`, `RateLimit`, `NewBreaker`, `ProductionHeaders` |
| `wsx`, `js` | WebSocket upgrades; JavaScript and TypeScript handlers run in goja |
| `LLM.md` | notes for changing zip itself |

## License

MIT. See [LICENSE](LICENSE).
