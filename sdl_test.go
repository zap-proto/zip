package zip

import (
	"context"
	"strings"
	"testing"
)

// The claim under test: a schema a graph publishes is one a client can build
// against, and CheckSchema says so — or names what is wrong — before a client
// finds out.

func TestASchemaWithEveryConstructChecks(t *testing.T) {
	sdl := `
"""The whole schema."""
schema @tag(name: "x") { query: Root mutation: Change }

"An opaque value."
scalar JSON

# A comment is not a definition.
interface Named { name: String! }

type Root {
  "One by id."
  thing(id: ID!, depth: Int = 2, mode: Mode = FAST, where: Filter): Thing @deprecated(reason: "use things")
  things(first: Int, after: String): [Thing!]!
  any: Any
  blob: JSON
}

type Thing implements Named & Other { name: String! size: Float tags: [[String!]] }
interface Other { size: Float }
union Any = | Thing | Change
enum Mode { FAST "Slow, but thorough." SLOW @deprecated }
input Filter { name: String = "x", mode: Mode, nested: Filter, ids: [ID!] = [] }
type Change { done: Boolean }
directive @tag(name: String!) repeatable on SCHEMA | OBJECT
`
	if err := CheckSchema(sdl); err != nil {
		t.Fatalf("a valid schema was refused: %v", err)
	}
}

func TestASchemaThatCannotBeBuiltAgainstIsNamed(t *testing.T) {
	for want, sdl := range map[string]string{
		"does not define":       `type Query { a: Missing }`,
		"defined twice":         `type Query { a: String } type Query { b: String }`,
		"field a twice":         `type Query { a: String a: Int }`,
		"is an input type":      `type Query { a: In } input In { x: Int }`,
		"an argument takes":     `type Query { a(x: Out): String } type Out { y: Int }`,
		"an input field takes":  `type Query { a(x: In): String } input In { o: Out } type Out { y: Int }`,
		"has no query type":     `type Mutation { a: String }`,
		"is not an object type": `schema { query: Q } scalar Q`,
		"has no fields":         `type Query { a: E } type E { }`,
		"not an object type":    `type Query { a: U } union U = String`,
		"not an interface":      `type Query implements Thing { a: String } type Thing { b: String }`,
		"takes the argument x":  `type Query { a(x: Int, x: Int): String }`,
		"expected : and a type": `type Query { a }`,
		"unterminated":          `type Query { a: String`,
		"`extend` is not read":  `type Query { a: String } extend type Query { b: String }`,
	} {
		err := CheckSchema(sdl)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s\n  checked as %v, want an error saying %q", sdl, err, want)
		}
	}
}

// The schema zip derives from an app's ops is held to the same rule. A map or an
// interface publishes as JSON, so JSON must be declared, or every app with one
// published a schema naming a type it never defined.
func TestTheDerivedSchemaChecks(t *testing.T) {
	type bag struct {
		Name  string         `json:"name"`
		Extra map[string]any `json:"extra"`
		Any   any            `json:"any"`
	}
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	app.Get("/v1/bags/:id", func(ctx context.Context, _ *resGet) (*bag, error) { return nil, nil },
		WithOperationID("bag"), WithSummary("One bag"))
	app.Post("/v1/bags", func(ctx context.Context, _ *resMake) (*bag, error) { return nil, nil },
		WithOperationID("makeBag"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	sdl := app.GraphQLSDL()
	if !strings.Contains(sdl, "scalar JSON") {
		t.Errorf("JSON is used and not declared:\n%s", sdl)
	}
	if err := CheckSchema(sdl); err != nil {
		t.Fatalf("zip's own schema does not check: %v\n%s", err, sdl)
	}
	if err := CheckSchema(resApp(t).GraphQLSDL()); err != nil {
		t.Fatalf("zip's own schema does not check: %v", err)
	}
}
