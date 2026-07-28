package zip_test

import (
	"context"
	"testing"

	"github.com/zap-proto/zip"
)

type quoteIn struct {
	Symbol string `json:"symbol" validate:"required"`
	Venue  string `json:"venue"`
}
type quoteOut struct {
	Cents int `json:"cents"`
}

type fillIn struct {
	OrderID string `json:"order_id" validate:"required"`
	Qty     int    `json:"qty"`
}
type fillOut struct {
	Filled bool `json:"filled"`
}

// TestMCPTools_AreTheRegistryProjected proves the MCP surface is derived, not
// authored: an app registers TWO typed ops and its entire tool list — names,
// input schemas and descriptions — falls out of that registry with no per-app
// MCP code and no transport in the way. App.MCPTools() is the tool-list
// counterpart of App.OpenAPISpec(): one registry, read directly.
func TestMCPTools_AreTheRegistryProjected(t *testing.T) {
	// What cmd/zipdoc emits for a documented handler. The doc COMMENT is the
	// description — a model choosing a tool reads the same prose a human reading
	// the spec does.
	zip.Describe("POST /v1/market/quote", zip.Doc{
		Description: "quote returns the current ask for a symbol. It reads the venue's book,\nnot the last trade: the last trade says what someone paid and only the book\nsays what you can pay now.",
		Fields: map[string]string{
			"quoteIn.symbol": "Symbol is the venue's ticker, not the ISIN.",
			"quoteIn.venue":  "Venue narrows the quote to one book; empty takes the best.",
		},
	})

	app := zip.New(zip.Config{AppName: "market", DisableStartupMessage: true})
	// Documented the canonical way: a doc comment, no WithSummary.
	zip.Post(app, "/v1/market/quote", func(_ context.Context, _ *quoteIn) (*quoteOut, error) {
		return &quoteOut{Cents: 1200}, nil
	}, zip.WithOperationID("market_quote"))
	// Undocumented: WithSummary still names it.
	zip.Post(app, "/v1/market/fill", func(_ context.Context, _ *fillIn) (*fillOut, error) {
		return &fillOut{Filled: true}, nil
	}, zip.WithOperationID("market_fill"), zip.WithSummary("Fill a resting order"))

	tools := app.MCPTools()
	if len(tools) != 2 {
		t.Fatalf("want one tool per typed op (2), got %d", len(tools))
	}

	byName := map[string]map[string]any{}
	for _, tool := range tools {
		byName[tool["name"].(string)] = tool
	}

	// --- names are the ops' operation ids -------------------------------------
	quote, ok := byName["market_quote"]
	if !ok {
		t.Fatalf("tool name must be the op's operationId; got %v", byName)
	}
	fill, ok := byName["market_fill"]
	if !ok {
		t.Fatalf("missing market_fill; got %v", byName)
	}

	// --- descriptions are the doc-comment prose zipdoc lifted -----------------
	if got := quote["description"].(string); got != "quote returns the current ask for a symbol. It reads the venue's book,\nnot the last trade: the last trade says what someone paid and only the book\nsays what you can pay now." {
		t.Fatalf("description is not the doc comment: %q", got)
	}
	if got := fill["description"].(string); got != "Fill a resting order" {
		t.Fatalf("WithSummary fallback lost: %q", got)
	}

	// --- inputSchema is In's JSON Schema, prose and all -----------------------
	qs := quote["inputSchema"].(map[string]any)
	if qs["type"] != "object" {
		t.Fatalf("inputSchema is not an object schema: %v", qs)
	}
	qprops := qs["properties"].(map[string]any)
	if len(qprops) != 2 || qprops["symbol"] == nil || qprops["venue"] == nil {
		t.Fatalf("inputSchema must carry every In field: %v", qprops)
	}
	if got := qprops["symbol"].(map[string]any)["description"]; got != "Symbol is the venue's ticker, not the ISIN." {
		t.Fatalf("field prose did not reach the schema: %v", got)
	}
	if req, _ := qs["required"].([]string); len(req) != 1 || req[0] != "symbol" {
		t.Fatalf(`validate:"required" must reach the schema: %v`, qs["required"])
	}

	// The second op's schema is its OWN In — the projection is per-op, not a
	// shared blob.
	fprops := fill["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if fprops["order_id"] == nil || fprops["qty"] == nil || fprops["symbol"] != nil {
		t.Fatalf("market_fill's schema is not fillIn: %v", fprops)
	}
	if got := fprops["qty"].(map[string]any)["type"]; got != "integer" {
		t.Fatalf("Go int must project as integer, got %v", got)
	}

	// --- the tool list and the OpenAPI document agree, op for op --------------
	// Both read the same registry, so a disagreement here means a projection
	// started deriving its own truth.
	spec := app.OpenAPISpec()
	paths := spec["paths"].(map[string]map[string]any)
	for path, name := range map[string]string{
		"/v1/market/quote": "market_quote",
		"/v1/market/fill":  "market_fill",
	} {
		post := paths[path]["post"].(map[string]any)
		if post["operationId"] != name {
			t.Fatalf("%s: spec operationId %v, tool name %q — one op, two names",
				path, post["operationId"], name)
		}
		if post["description"] != nil && post["description"] != byName[name]["description"] {
			t.Fatalf("%s: spec and tool describe the same op differently:\n spec=%q\n tool=%q",
				path, post["description"], byName[name]["description"])
		}
	}
}

// TestMCPTools_EveryToolIsCallableOverTheCallPlane ties the two by-name
// surfaces together: every tool the MCP projection advertises is invocable by
// that same name over the op-call plane, because both resolve through the one
// registry lookup. A name that lists but cannot be called would mean the two
// projections had drifted apart.
func TestMCPTools_EveryToolIsCallableOverTheCallPlane(t *testing.T) {
	app := flagsApp(t)
	sock := serveUDS(t, app)

	c, err := zip.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	tools := app.MCPTools()
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	for _, tool := range tools {
		name := tool["name"].(string)
		// A 404 here means the name the tool list advertises is not the name the
		// call plane resolves. Any other outcome (including a validation error)
		// proves the op was found.
		_, err := zip.Call[boolIn, boolOut](context.Background(), c, name, &boolIn{Flag: "beta"})
		var he *zip.HTTPError
		if err != nil && asHTTPError(err, &he) && he.Status == 404 {
			t.Fatalf("tool %q lists but does not resolve on the call plane", name)
		}
	}
}

func asHTTPError(err error, dst **zip.HTTPError) bool {
	he, ok := err.(*zip.HTTPError)
	if ok {
		*dst = he
	}
	return ok
}
