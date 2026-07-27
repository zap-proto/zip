package zip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// ---------------------------------------------------------------------------
// The types one service registers. Nothing here is CLI-specific: these are the
// ordinary In/Out types of ordinary typed handlers, and everything the command
// line shows is read back off them.
// ---------------------------------------------------------------------------

type listAppsIn struct {
	Env    string `json:"env"`
	Health string `json:"health"`
	Drift  bool   `json:"drift"`
	Limit  int    `json:"limit"`
}
type listAppsOut struct {
	Apps []string `json:"apps"`
}

type deployIn struct {
	App  string `json:"app"`
	Env  string `json:"env" validate:"required"`
	Wait bool   `json:"wait"`
}
type deployOut struct {
	App       string `json:"app"`
	Env       string `json:"env"`
	Restarted bool   `json:"restarted"`
}

// TestCLI_NameComesFromTheRoute pins the derivation of `<service> <operation>`
// from an op's identity. The name is a function of that one op, so adding an
// unrelated route can never rename an existing command.
func TestCLI_NameComesFromTheRoute(t *testing.T) {
	cases := []struct {
		method, path string
		service, op  string
	}{
		{"GET", "/v1/paas/apps", "paas", "apps-list"},
		{"GET", "/v1/paas/apps/:app", "paas", "apps-get"},
		{"POST", "/v1/paas/apps/:app/deploy", "paas", "apps-deploy"},
		{"GET", "/v1/paas/apps/:app/deploy", "paas", "apps-deploy-get"},
		{"POST", "/v1/billing/charge", "billing", "charge-create"},
		{"GET", "/v1/billing/invoices", "billing", "invoices-list"},
		{"DELETE", "/v1/keys/:id", "keys", "delete"},
		{"POST", "/v1/keys", "keys", "create"},
		{"PATCH", "/v2/crm/contacts/:id", "crm", "contacts-update"},
		{"GET", "/v1/gpu/nodes/:node/jobs", "gpu", "nodes-jobs-list"},
	}
	for _, c := range cases {
		app := zip.New(zip.Config{DisableStartupMessage: true})
		register(app, c.method, c.path)
		cmds := app.Commands()
		if len(cmds) != 1 {
			t.Fatalf("%s %s: got %d commands, want 1", c.method, c.path, len(cmds))
		}
		if cmds[0].Service != c.service || cmds[0].Name != c.op {
			t.Errorf("%s %s → %q %q, want %q %q",
				c.method, c.path, cmds[0].Service, cmds[0].Name, c.service, c.op)
		}
	}
}

// register adds one typed route by method, so the name table above can be a
// table rather than ten near-identical apps.
func register(app *zip.App, method, path string) {
	fn := func(_ context.Context, _ *listAppsIn) (*listAppsOut, error) { return &listAppsOut{}, nil }
	switch method {
	case "GET":
		zip.Get(app, path, fn)
	case "POST":
		zip.Post(app, path, fn)
	case "PUT":
		zip.Put(app, path, fn)
	case "PATCH":
		zip.Patch(app, path, fn)
	case "DELETE":
		zip.Delete(app, path, fn)
	}
}

// TestCLI_FlagsAndHelpComeFromTheSource proves the whole command — arguments,
// flags, types, required-ness, prose and example — is read off the handler's
// types and its doc comment. None of it is written for the CLI.
func TestCLI_FlagsAndHelpComeFromTheSource(t *testing.T) {
	app := deployApp(t)

	var cmd zip.Command
	for _, c := range app.Commands() {
		if c.Service == "paas" && c.Name == "apps-deploy" {
			cmd = c
		}
	}
	if cmd.Name == "" {
		t.Fatal("apps-deploy was not derived")
	}

	// The path parameter addresses the resource, so it is positional — and it is
	// NOT also a flag, because that would be two ways to set one value.
	if len(cmd.Args) != 1 || cmd.Args[0].Name != "app" {
		t.Fatalf("args = %+v, want one <app>", cmd.Args)
	}
	if cmd.Args[0].Help != "App to redeploy." {
		t.Errorf("arg help = %q, want the field's doc comment", cmd.Args[0].Help)
	}

	want := map[string]zip.Flag{
		"env":  {Name: "env", Field: "env", Type: "string", Help: "Lifecycle env: main|test|dev.", Required: true},
		"wait": {Name: "wait", Field: "wait", Type: "boolean", Help: "Block until the rollout settles."},
	}
	if len(cmd.Flags) != len(want) {
		t.Fatalf("flags = %+v, want %d", cmd.Flags, len(want))
	}
	for _, got := range cmd.Flags {
		w, ok := want[got.Name]
		if !ok {
			t.Fatalf("unexpected flag --%s", got.Name)
		}
		if got != w {
			t.Errorf("--%s = %+v, want %+v", got.Name, got, w)
		}
	}

	// The summary is the doc comment's first sentence — Go's own convention
	// already makes that a summary, so nobody writes a second one.
	if !strings.HasPrefix(cmd.Summary, "Deploy restarts an app") || strings.Contains(cmd.Summary, "zero downtime") {
		t.Errorf("summary = %q, want just the first sentence", cmd.Summary)
	}

	// The help renders the doc comment's own Example body as the command line
	// that sends it: one example, two projections.
	var out bytes.Buffer
	cli := &zip.CLI{Name: "hanzo", Commands: app.Commands(), Out: &out}
	if err := cli.Run(context.Background(), []string{"paas", "apps-deploy", "--help"}); err != nil {
		t.Fatalf("help: %v", err)
	}
	help := out.String()
	for _, want := range []string{
		"hanzo paas apps-deploy <app>",
		"Deploy restarts an app",
		"zero downtime",                 // the full description, not just the summary
		"--env string",                  // flag name and type
		"Lifecycle env: main|test|dev.", // the field's doc comment
		"(required)",
		"--wait boolean",
		"POST /v1/paas/apps/:app/deploy",
		"hanzo paas apps-deploy acme --env main --wait", // the Example, as a command
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q\n---\n%s", want, help)
		}
	}
}

// deployApp is one service: two typed routes and the extraction cmd/zipdoc
// emits for them.
func deployApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "hanzo", DisableStartupMessage: true})

	zip.Describe("POST /v1/paas/apps/:app/deploy", zip.Doc{
		Description: "Deploy restarts an app. The rollout is a rolling restart, so it happens with zero downtime.",
		Fields: map[string]string{
			"deployIn.app":  "App to redeploy.",
			"deployIn.env":  "Lifecycle env: main|test|dev.",
			"deployIn.wait": "Block until the rollout settles.",
		},
		Example:  json.RawMessage(`{"app":"acme","env":"main","wait":true}`),
		Response: json.RawMessage(`{"app":"acme","env":"main","restarted":true}`),
	})
	zip.Post(app, "/v1/paas/apps/:app/deploy", func(_ context.Context, in *deployIn) (*deployOut, error) {
		return &deployOut{App: in.App, Env: in.Env, Restarted: in.Wait}, nil
	})

	zip.Describe("GET /v1/paas/apps", zip.Doc{
		Description: "ListApps returns the fleet board.",
		Fields:      map[string]string{"listAppsIn.env": "Filter by env."},
	})
	zip.Get(app, "/v1/paas/apps", func(_ context.Context, in *listAppsIn) (*listAppsOut, error) {
		return &listAppsOut{Apps: []string{"acme:" + in.Env}}, nil
	})
	return app
}

// TestCLI_RunsTheRegisteredHandler proves a command IS the operation: the
// positional argument reaches the In field the path names, the flags reach
// theirs, and the handler's own output is what the CLI prints.
func TestCLI_RunsTheRegisteredHandler(t *testing.T) {
	app := deployApp(t)
	var out bytes.Buffer
	cli := app.CLI()
	cli.Out = &out

	if err := cli.Run(context.Background(), []string{"paas", "apps-deploy", "acme", "--env", "main", "--wait"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got deployOut
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if got.App != "acme" || got.Env != "main" || !got.Restarted {
		t.Fatalf("handler saw %+v, want the argument and both flags", got)
	}
}

// TestCLI_RefusesWhatTheTypeRefuses proves the CLI enforces exactly what the
// operation declares — no more, and nothing of its own.
func TestCLI_RefusesWhatTheTypeRefuses(t *testing.T) {
	app := deployApp(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"paas", "apps-deploy", "acme"}, "--env is required"},
		{[]string{"paas", "apps-deploy", "acme", "--env", "main", "--nope", "x"}, "unknown flag --nope"},
		{[]string{"paas", "apps-deploy", "--env", "main"}, "takes 1 argument"},
		{[]string{"paas", "apps-list", "--limit", "many"}, "--limit wants an integer"},
		{[]string{"paas", "nope"}, `unknown operation "nope"`},
		{[]string{"nope"}, `unknown service "nope"`},
	}
	for _, c := range cases {
		var out bytes.Buffer
		cli := app.CLI()
		cli.Out = &out
		err := cli.Run(context.Background(), c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("run %v = %v, want an error containing %q", c.args, err, c.want)
		}
	}
}

// TestCLI_ANewRouteIsANewCommand is the test that decides whether the CLI is
// really derived: register one more typed route and the command exists, with
// its flags and its help, having edited no CLI source at all.
func TestCLI_ANewRouteIsANewCommand(t *testing.T) {
	app := deployApp(t)
	before := len(app.Commands())
	for _, c := range app.Commands() {
		if c.Service == "billing" {
			t.Fatalf("billing already exists")
		}
	}

	// The one edit: a service adds a typed route.
	type refundIn struct {
		Invoice string `json:"invoice"`
		Cents   int    `json:"cents" validate:"required"`
		Reason  string `json:"reason"`
	}
	type refundOut struct {
		Refunded int `json:"refunded"`
	}
	zip.Describe("POST /v1/billing/invoices/:invoice/refund", zip.Doc{
		Description: "Refund returns money for an invoice.",
		Fields:      map[string]string{"refundIn.cents": "Amount to refund, in cents."},
		Example:     json.RawMessage(`{"invoice":"inv_1","cents":1200}`),
	})
	zip.Post(app, "/v1/billing/invoices/:invoice/refund",
		func(_ context.Context, in *refundIn) (*refundOut, error) {
			return &refundOut{Refunded: in.Cents}, nil
		})

	cmds := app.Commands()
	if len(cmds) != before+1 {
		t.Fatalf("commands went %d → %d, want one more", before, len(cmds))
	}
	var got zip.Command
	for _, c := range cmds {
		if c.Service == "billing" && c.Name == "invoices-refund" {
			got = c
		}
	}
	if got.Name == "" {
		t.Fatalf("the new route produced no `billing invoices-refund`: %v", names(cmds))
	}
	if len(got.Args) != 1 || got.Args[0].Name != "invoice" {
		t.Errorf("args = %+v, want <invoice>", got.Args)
	}
	if len(got.Flags) != 2 {
		t.Fatalf("flags = %+v, want --cents and --reason", got.Flags)
	}
	for _, f := range got.Flags {
		if f.Name == "cents" && (!f.Required || f.Type != "integer" || f.Help != "Amount to refund, in cents.") {
			t.Errorf("--cents = %+v, want a required integer with its doc comment", f)
		}
	}

	// And it runs.
	var out bytes.Buffer
	cli := app.CLI()
	cli.Out = &out
	if err := cli.Run(context.Background(), []string{"billing", "invoices-refund", "inv_1", "--cents", "1200"}); err != nil {
		t.Fatalf("run the new command: %v", err)
	}
	if !strings.Contains(out.String(), `"refunded": 1200`) {
		t.Fatalf("output = %q, want the handler's result", out.String())
	}
}

func names(cmds []zip.Command) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, c.Service+" "+c.Name)
	}
	return out
}

// TestCLI_SpecAndRegistryAgree proves the two derivations are one derivation:
// the command tree read from the live registry and the command tree read from
// the document that registry generates are the same commands.
//
// The flags of a GET are compared only where the document carries them. A zip
// document describes what the WIRE accepts, and a typed GET's non-path inputs
// are not on the wire yet (they belong in the query string) — so the client
// tree offers what the service will actually honour, which is the honest thing
// for it to offer.
func TestCLI_SpecAndRegistryAgree(t *testing.T) {
	app := deployApp(t)
	spec, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	fromSpec, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	fromRegistry := app.Commands()
	if len(fromSpec) != len(fromRegistry) {
		t.Fatalf("spec has %v, registry has %v", names(fromSpec), names(fromRegistry))
	}
	for i := range fromRegistry {
		r, s := fromRegistry[i], fromSpec[i]
		if r.Service != s.Service || r.Name != s.Name || r.Method != s.Method || r.Path != s.Path {
			t.Fatalf("op %d: spec says %q %q (%s %s), registry says %q %q (%s %s)",
				i, s.Service, s.Name, s.Method, s.Path, r.Service, r.Name, r.Method, r.Path)
		}
		if fmt.Sprint(r.Args) != fmt.Sprint(s.Args) {
			t.Errorf("%s %s args: spec %v, registry %v", r.Service, r.Name, s.Args, r.Args)
		}
		if r.Summary != s.Summary {
			t.Errorf("%s %s summary: spec %q, registry %q", r.Service, r.Name, s.Summary, r.Summary)
		}
		if r.Method == "GET" || r.Method == "HEAD" || r.Method == "DELETE" {
			continue
		}
		if fmt.Sprint(r.Flags) != fmt.Sprint(s.Flags) {
			t.Errorf("%s %s flags: spec %v, registry %v", r.Service, r.Name, s.Flags, r.Flags)
		}
		if string(r.Example) != string(s.Example) {
			t.Errorf("%s %s example: spec %s, registry %s", r.Service, r.Name, s.Example, r.Example)
		}
	}
}

// TestCLI_RemoteRunsAgainstARunningService is the client case end to end: a
// binary that links none of the service's code asks it what it can do, builds
// the command line from the answer, and runs a command against it.
func TestCLI_RemoteRunsAgainstARunningService(t *testing.T) {
	app := deployApp(t)
	addr := freeAddr(t)
	go func() { _ = app.Listen("http://" + addr) }()
	defer func() { _ = app.Shutdown() }()
	waitHTTP(t, "http://"+addr+"/.well-known/openapi.json")

	// Everything from here knows only an address.
	remote := zip.Remote{Base: "http://" + addr, Header: map[string]string{"X-Test": "1"}}
	spec, err := remote.Spec(context.Background())
	if err != nil {
		t.Fatalf("fetch spec: %v", err)
	}
	cmds, err := zip.CommandsFromSpec(spec)
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	var out bytes.Buffer
	cli := &zip.CLI{Name: "hanzo", Commands: cmds, Invoke: remote.Invoke, Out: &out}

	if err := cli.Run(context.Background(), []string{"paas", "apps-deploy", "acme", "--env", "main", "--wait"}); err != nil {
		t.Fatalf("remote run: %v", err)
	}
	var got deployOut
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if got.App != "acme" || got.Env != "main" || !got.Restarted {
		t.Fatalf("service saw %+v, want the argument and both flags", got)
	}

	// A required flag is enforced before anything leaves the machine — the
	// document carries validate:"required" through as the schema's required.
	out.Reset()
	if err := cli.Run(context.Background(), []string{"paas", "apps-deploy", "acme"}); err == nil ||
		!strings.Contains(err.Error(), "--env is required") {
		t.Fatalf("missing --env = %v, want the client to refuse it", err)
	}

	// A spec-derived command has no handler in this process, and says so rather
	// than pretending.
	out.Reset()
	local := &zip.CLI{Name: "hanzo", Commands: cmds, Invoke: zip.LocalInvoke, Out: &out}
	if err := local.Run(context.Background(), []string{"paas", "apps-deploy", "acme", "--env", "main"}); err == nil ||
		!strings.Contains(err.Error(), "not registered in this process") {
		t.Fatalf("local invoke of a remote command = %v, want a clear refusal", err)
	}
}

// TestCLI_UsageListsServices proves the top level is derived too: no list of
// services is written anywhere.
func TestCLI_UsageListsServices(t *testing.T) {
	app := deployApp(t)
	var out bytes.Buffer
	cli := app.CLI()
	cli.Out = &out
	if err := cli.Run(context.Background(), nil); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !strings.Contains(out.String(), "paas") || !strings.Contains(out.String(), "2 operations") {
		t.Fatalf("usage = %q, want the derived service list", out.String())
	}

	out.Reset()
	if err := cli.Run(context.Background(), []string{"paas"}); err != nil {
		t.Fatalf("service usage: %v", err)
	}
	for _, want := range []string{"apps-deploy <app>", "apps-list", "Deploy restarts an app"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("service usage is missing %q\n---\n%s", want, out.String())
		}
	}
}
