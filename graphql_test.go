package zip

import (
	"context"
	"strings"
	"testing"
)

// The claim under test: a GraphQL schema is DERIVED, never written. Nothing here
// declares a field — the fields exist because ops do, which is the whole reason
// to project rather than maintain a second description that can disagree.

type gqlUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Age   int    `json:"age"`
}

type gqlGetIn struct {
	ID string `json:"id" validate:"required"`
}

type gqlMakeIn struct {
	Email string `json:"email" validate:"required"`
	Note  string `json:"note"`
}

func gqlApp(t *testing.T) *App {
	t.Helper()
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	Get(app, "/v1/users/:id", func(ctx context.Context, _ *gqlGetIn) (*gqlUser, error) {
		return &gqlUser{}, nil
	}, WithOperationID("user"), WithSummary("One user by id"))
	Post(app, "/v1/users", func(ctx context.Context, _ *gqlMakeIn) (*gqlUser, error) {
		return &gqlUser{}, nil
	}, WithOperationID("createUser"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

// A safe read is a Query and everything else is a Mutation, because the method is
// the only signal an op gives about whether it changes anything. Getting this
// backwards would put a write behind a cache.
func TestReadsAreQueriesAndWritesAreMutations(t *testing.T) {
	s := gqlApp(t).GraphQLSDL()

	q := section(s, "type Query {")
	m := section(s, "type Mutation {")
	if !strings.Contains(q, "user(") {
		t.Errorf("the GET op is not a Query field:\n%s", q)
	}
	if strings.Contains(q, "createUser") {
		t.Errorf("a POST op was published as a Query — a write reachable as a read:\n%s", q)
	}
	if !strings.Contains(m, "createUser(") {
		t.Errorf("the POST op is not a Mutation field:\n%s", m)
	}
}

// The op's In IS its argument list. Flattening it rather than wrapping it in an
// `input:` object is what makes the GraphQL call read like the same call over
// REST or the CLI.
func TestArgumentsComeFromTheInputType(t *testing.T) {
	s := gqlApp(t).GraphQLSDL()
	q := section(s, "type Query {")
	if !strings.Contains(q, "id: String!") {
		t.Errorf("`id` is absent or not required — validate:\"required\" must carry:\n%s", q)
	}
	m := section(s, "type Mutation {")
	if !strings.Contains(m, "email: String!") {
		t.Errorf("required input field lost its !:\n%s", m)
	}
	if !strings.Contains(m, "note: String") || strings.Contains(m, "note: String!") {
		t.Errorf("an optional field was marked required:\n%s", m)
	}
}

// The Out type becomes a named object, defined once however many ops answer with
// it — two ops sharing a type must not produce two definitions of it.
func TestTheResultTypeIsDefinedExactlyOnce(t *testing.T) {
	s := gqlApp(t).GraphQLSDL()
	if n := strings.Count(s, "type GqlUser {"); n != 1 {
		t.Fatalf("gqlUser defined %d times, want exactly 1:\n%s", n, s)
	}
	body := section(s, "type GqlUser {")
	for _, f := range []string{"id: String", "email: String", "age: Int"} {
		if !strings.Contains(body, f) {
			t.Errorf("missing %q in the result type:\n%s", f, body)
		}
	}
}

// A summary is documentation the op already carries. Dropping it would make the
// generated schema less useful than the OpenAPI document built from the same op.
func TestTheSummaryBecomesTheDescription(t *testing.T) {
	s := gqlApp(t).GraphQLSDL()
	if !strings.Contains(s, `"""One user by id"""`) {
		t.Errorf("the op summary did not reach the schema:\n%s", s)
	}
}

// A struct that refers to itself is reached again while its own definition is
// still being written. Without a guard that is unbounded recursion, so this is
// the test that says the guard is real rather than incidental.
func TestASelfReferentialTypeTerminates(t *testing.T) {
	type node struct {
		Name string `json:"name"`
		Next *node  `json:"next"`
	}
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	Get(app, "/v1/tree", func(ctx context.Context, _ *gqlGetIn) (*node, error) { return nil, nil },
		WithOperationID("tree"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	s := app.GraphQLSDL() // hangs or overflows if the cycle guard is absent
	if n := strings.Count(s, "type Node {"); n != 1 {
		t.Fatalf("Node defined %d times, want 1:\n%s", n, s)
	}
	if !strings.Contains(section(s, "type Node {"), "next: Node") {
		t.Errorf("the self-reference did not survive:\n%s", s)
	}
}

// GraphQL has no untyped node. A map has to be named something honest rather than
// given an invented shape a client would then rely on.
func TestAnUndescribableFieldIsNotGivenAnInventedShape(t *testing.T) {
	type blob struct {
		Meta map[string]any `json:"meta"`
	}
	app := New(Config{AppName: "svc", DisableStartupMessage: true})
	Get(app, "/v1/blob", func(ctx context.Context, _ *gqlGetIn) (*blob, error) { return nil, nil },
		WithOperationID("blob"))
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(app.GraphQLSDL(), "meta: JSON") {
		t.Errorf("a map[string]any was described as something it is not:\n%s", app.GraphQLSDL())
	}
}

// section returns the block that starts at head, up to the closing brace.
func section(s, head string) string {
	i := strings.Index(s, head)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	if j := strings.Index(rest, "\n}"); j >= 0 {
		return rest[:j]
	}
	return rest
}
