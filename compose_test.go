package zip

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// These are internal tests on purpose. The walk, the seal and the occurrence
// slice are the contract every projection is written against, and they are
// unexported — the AST is not a compatibility surface. Testing them from
// zip_test would mean exporting the AST to test it, which is the tail wagging
// the dog.

func quiet(name string) *App {
	return New(Config{AppName: name, DisableStartupMessage: true})
}

type invoiceIn struct {
	ID string `json:"id"`
}
type invoiceOut struct {
	ID    string `json:"id"`
	Total int    `json:"total"`
}

// billingApp is one DEFINITION, written once, included wherever a test needs it.
// Its op DECLARES its id, which is the shape every real service in the estate
// has — and a declared id survives composition verbatim (see [occurrenceID]),
// so this definition may be included exactly ONCE in any one program.
func billingApp() *App {
	b := quiet("billing")
	Get(b, "/invoices/:id", func(_ context.Context, in *invoiceIn) (*invoiceOut, error) {
		return &invoiceOut{ID: in.ID, Total: 42}, nil
	}, WithOperationID("listInvoices"))
	return b
}

// unnamedApp is the same definition with its op's id left UNDECLARED, and it is
// what the diamond tests need.
//
// The two fixtures are not a duplication, they are the two halves of one rule.
// An id the author WROTE DOWN is a published name and composition may not edit
// it, so a definition carrying one cannot occur twice — [derived] refuses that,
// by design. An id nobody wrote down carries no promise, so the walk derives it
// from the occurrence's prefix, and THAT is what makes one definition includable
// at two addresses. Every test below about the diamond is a test about the
// second half, and must use a definition that has no name to keep.
//
// Its id is derived per occurrence from the ABSOLUTE path it answers at, which
// is what distinguishes the two: ID("GET", "/v1/invoices/:id") is
// get_invoices_by_id, and under /admin it is get_admin_invoices_by_id.
func unnamedApp() *App {
	b := quiet("billing")
	Get(b, "/invoices/:id", func(_ context.Context, in *invoiceIn) (*invoiceOut, error) {
		return &invoiceOut{ID: in.ID, Total: 42}, nil
	})
	return b
}

// mwAt reduces a traversal to the size of the middleware stack in force at one
// absolute path, or -1 when nothing answers there.
func mwAt(a *App, path string) int {
	got := -1
	occ, _ := walk(a)
	for _, o := range occ {
		if r, ok := o.route(); ok && o.abs(r.path) == path {
			got = o.ctx.mw.len()
		}
	}
	return got
}

// kinds renders a traversal as a flat list, by REDUCING it — the walk hands the
// callback parameters and retains nothing, so a test that wants a sequence
// builds its own.
func kinds(a *App) []string {
	var out []string
	occ, _ := walk(a)
	for _, o := range occ {
		switch o.kind() {
		case kindMiddleware:
			out = append(out, "mw")
		case kindRoute:
			r, _ := o.route()
			out = append(out, r.method+" "+o.abs(r.path))
		case kindApp:
			def, _ := o.app()
			out = append(out, "app:"+def.label()+"@"+o.ctx.prefix)
		}
	}
	return out
}

// ── the walk ────────────────────────────────────────────────────────────────

// TestWalk_OrderIsProgramOrder is the property everything else rests on: the
// occurrence slice is the program read top to bottom, with each included
// definition read IN PLACE at its inclusion site. Two typed slices — one of
// middleware, one of children — cannot express this, and the interleaving is
// semantic: "requestID came before users, authz came after" is a fact about
// what runs, not about how the framework stored it.
func TestWalk_OrderIsProgramOrder(t *testing.T) {
	users := quiet("users")
	users.Get("/users", func(c *Ctx) error { return nil })

	root := quiet("root")
	root.Use(H(func(c *Ctx) error { return c.Continue() })) // requestID
	root.Use(users)
	root.Use(H(func(c *Ctx) error { return c.Continue() })) // authz
	root.Get("/late", func(c *Ctx) error { return nil })

	want := []string{"mw", "app:users@", "GET /users", "mw", "GET /late"}
	got := kinds(root)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("walk order:\n got %v\nwant %v", got, want)
	}
}

// TestWalk_AppendIsStable pins the guarantee a generated SDK depends on:
// composing one more thing cannot reorder what was already composed. Without
// it, adding a plugin reshuffles operation order in the OpenAPI document and
// every downstream artifact churns for no semantic reason.
func TestWalk_AppendIsStable(t *testing.T) {
	a, b := quiet("a"), quiet("b")
	a.Get("/a", func(c *Ctx) error { return nil })
	b.Get("/b", func(c *Ctx) error { return nil })

	root := quiet("root")
	root.Use(a)
	before := mustWalk(t, root)

	root.Use(b)
	after := mustWalk(t, root)

	if len(after) <= len(before) {
		t.Fatalf("appending b did not add occurrences: %v -> %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("appending b reordered occurrence %d: %q -> %q\n%v\n%v",
				i, before[i], after[i], before, after)
		}
	}
}

// TestWalk_IsDeterministic: same program, identical slice. A projection that is
// not a pure function of the program is a projection whose output diffs for
// reasons nobody can explain.
func TestWalk_IsDeterministic(t *testing.T) {
	root := quiet("root")
	root.Use(H(func(c *Ctx) error { return nil }))
	root.Group("/v1").Use(billingApp())

	first := mustWalk(t, root)
	for i := 0; i < 5; i++ {
		if got := mustWalk(t, root); strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("walk %d differs:\n %v\n %v", i, got, first)
		}
	}
}

// build is what every test that needs a live program calls: it is exactly what
// Listen does, minus the listener — construct generation N+1, validate it, and
// install it only if it is valid.
func build(a *App) error {
	a.buildMu.Lock()
	defer a.buildMu.Unlock()
	g, err := a.build()
	if err != nil {
		return err
	}
	a.install(g)
	return nil
}

func mustWalk(t *testing.T, a *App) []string {
	t.Helper()
	if err := verify(a); err != nil {
		t.Fatalf("verify: %v", err)
	}
	return kinds(a)
}

// ── snapshot semantics ──────────────────────────────────────────────────────

// TestSnapshot_ParentMiddlewareAfterInclusionDoesNotReachTheSubtree is clause
// (a), and it is where this model DIVERGES from Fiber. Under Fiber the stack is
// whatever had been registered by the time a route was, so a Use written later
// in the wiring file reaches a group created earlier. Here the subtree's
// environment is anchored at its inclusion site, so it does not.
//
// The divergence is the point: it makes a subtree's auth seam a function of
// where it was included, not of how many lines were added to the wiring file
// afterwards.
func TestSnapshot_ParentMiddlewareAfterInclusionDoesNotReachTheSubtree(t *testing.T) {
	root := quiet("root")
	v1 := root.Group("/v1")
	root.Use(H(func(c *Ctx) error { return c.Continue() })) // written AFTER v1 was included
	v1.Get("/x", func(c *Ctx) error { return nil })         // written after that

	switch n := mwAt(root, "/v1/x"); n {
	case -1:
		t.Fatal("/v1/x not found in the walk")
	case 0: // correct: the parent's later Use did not reach in
	default:
		t.Fatalf("/v1/x sees %d middleware; the parent's later Use reached into the subtree", n)
	}
}

// TestSnapshot_LateSubtreeRegistrationInheritsTheAnchoredStack is clause (b):
// the subtree's CONTENTS may grow until seal, its ENVIRONMENT may not. A route
// written into a group long after the group was included still gets the group's
// stack — WHERE it is written decides, not WHEN.
func TestSnapshot_LateSubtreeRegistrationInheritsTheAnchoredStack(t *testing.T) {
	root := quiet("root")
	root.Use(H(func(c *Ctx) error { return c.Continue() })) // anchored before v1
	v1 := root.Group("/v1")
	v1.Use(H(func(c *Ctx) error { return c.Continue() })) // the group's own
	root.Get("/other", func(c *Ctx) error { return nil })
	v1.Get("/late", func(c *Ctx) error { return nil }) // written last, still in v1

	if n := mwAt(root, "/v1/late"); n != 2 {
		t.Fatalf("/v1/late sees %d middleware, want 2 (root's, anchored, + the group's)", n)
	}
}

// TestLint_StagedCompositionIsReportedNotRefused. Middleware appended after an
// inclusion is legal and means what it says. It is INTENTIONAL co-located and a
// latent bug written far apart, and no walk can tell those apart — the
// difference is authorial intent, evidenced only by co-location. So it is a
// lint that names both sites and lets a human read them, rather than an error
// that would break the legitimate case or a silence that would hide the other.
func TestLint_StagedCompositionIsReportedNotRefused(t *testing.T) {
	root := quiet("root")
	root.Use(billingApp())
	root.Use(H(func(c *Ctx) error { return c.Continue() }))

	lints := root.Lint()
	if len(lints) != 1 {
		t.Fatalf("want 1 lint, got %d: %v", len(lints), lints)
	}
	for _, want := range []string{"staged composition", "billing", "compose_test.go:"} {
		if !strings.Contains(lints[0], want) {
			t.Errorf("lint does not mention %q: %s", want, lints[0])
		}
	}
	// And it is a lint, not a refusal: the program still builds.
	if err := build(root); err != nil {
		t.Fatalf("staged composition was refused: %v", err)
	}
}

// ── the diamond ─────────────────────────────────────────────────────────────

// TestDiamond_TwoOccurrencesTwoIdsOneType is the case the eager registry could
// not express at all, and the one that decides whether the lazy one is usable.
//
// One definition, included twice. The SURFACE doubles — two paths, two
// operation ids — and the TYPES do not: one Invoice, not two identical copies
// under two names. Surface keys on the occurrence, types key on the definition.
//
// The ids are derived from the PREFIX and never from position. "First
// occurrence wins" and "append -2" both make generated output a function of
// mount order, so swapping two lines in a wiring file becomes a breaking change
// in every published SDK.
func TestDiamond_TwoOccurrencesTwoIdsOneType(t *testing.T) {
	billing := unnamedApp() // ONE definition, no declared id — see unnamedApp
	root := quiet("cloud")
	root.Group("/v1").Use(billing)
	root.Group("/admin").Use(billing)

	if err := build(root); err != nil {
		t.Fatalf("build: %v", err)
	}

	var ids, paths []string
	for _, op := range root.Registry() {
		ids = append(ids, op.OperationID)
		paths = append(paths, op.Path)
	}
	wantIDs := []string{"get_invoices_by_id", "get_admin_invoices_by_id"}
	wantPaths := []string{"/v1/invoices/:id", "/admin/invoices/:id"}
	if strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("operation ids = %v, want %v", ids, wantIDs)
	}
	if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
		t.Errorf("paths = %v, want %v", paths, wantPaths)
	}

	// ONE type. Both occurrences point at the SAME reflect.Type, because the
	// definition is shared and only the surface was copied.
	reg := root.Registry()
	if reg[0].OutType != reg[1].OutType {
		t.Errorf("two occurrences produced two types: %v vs %v", reg[0].OutType, reg[1].OutType)
	}
	schemas := root.OpenAPISpec()["components"].(map[string]any)["schemas"].(map[string]any)
	n := 0
	for name := range schemas {
		if strings.HasSuffix(name, "invoiceOut") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("invoiceOut published %d times, want 1: %v", n, keysOfSchemas(schemas))
	}

	// And the document is valid: two operations, two distinct ids.
	if ids[0] == ids[1] {
		t.Fatal("both occurrences want one operationId — the document is invalid")
	}
}

// TestDiamond_IdsDoNotDependOnMountOrder: reversing the two inclusions must
// produce the same two ids. If it did not, reordering a wiring file would be an
// SDK break.
func TestDiamond_IdsDoNotDependOnMountOrder(t *testing.T) {
	idsFor := func(first, second string) map[string]bool {
		billing := unnamedApp()
		root := quiet("cloud")
		root.Group(first).Use(billing)
		root.Group(second).Use(billing)
		out := map[string]bool{}
		for _, op := range root.Registry() {
			out[op.OperationID] = true
		}
		return out
	}
	a := idsFor("/v1", "/admin")
	b := idsFor("/admin", "/v1")
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("want 2 ids each, got %v and %v", a, b)
	}
	for id := range a {
		if !b[id] {
			t.Fatalf("id %q appears in one order and not the other: %v vs %v", id, a, b)
		}
	}
}

func keysOfSchemas(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── cycles ──────────────────────────────────────────────────────────────────

// TestCycle_IsAnErrorWithABreadcrumbNotAHang. The moment an App is a node, a
// cycle is expressible, and without detection it is not a bad error message —
// it is an infinite descent. The ancestor set is what makes it an error, and
// the breadcrumb is what makes the error actionable.
func TestCycle_IsAnErrorWithABreadcrumbNotAHang(t *testing.T) {
	a, b := quiet("a"), quiet("b")
	a.Use(b)
	b.Use(a)

	err := build(a)
	if err == nil {
		t.Fatal("a cycle built successfully")
	}
	if !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "a → b → a") {
		t.Fatalf("cycle error has no breadcrumb: %v", err)
	}
}

// A definition that includes ITSELF is the one-node cycle.
func TestCycle_SelfInclusion(t *testing.T) {
	a := quiet("a")
	a.Use(a)
	if err := build(a); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("self-inclusion: %v", err)
	}
}

// TestDiamond_IsNotACycle: the ancestor set, not a global visited set. A
// definition included twice as SIBLINGS is the diamond, which is legal and is
// the whole point; only a definition included under ITSELF is a cycle.
func TestDiamond_IsNotACycle(t *testing.T) {
	shared := unnamedApp() // no declared id, so it may occur twice
	root := quiet("root")
	root.Group("/v1").Use(shared)
	root.Group("/admin").Use(shared)
	if err := build(root); err != nil {
		t.Fatalf("a diamond was refused as a cycle: %v", err)
	}
}

// ── the seal ────────────────────────────────────────────────────────────────

// TestFreeze_PropagatesAcrossTheGraph. Sealing only the app Listen was called on
// leaves exactly the race it was meant to close: the child is still writable
// while the parent is already serving what it published.
func TestFreeze_PropagatesAcrossTheGraph(t *testing.T) {
	users := quiet("users")
	admin := quiet("admin")
	users.Use(admin)
	root := quiet("root")
	root.Use(users)

	if err := build(root); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, a := range []*App{root, users, admin} {
		if !a.Frozen() {
			t.Errorf("%s is not frozen — the freeze did not reach the whole graph", a.label())
		}
	}
}

// TestFreeze_MutateAfterBuildPanics_IncludeAfterBuildSucceeds. Sealed is MONOTONIC
// and it freezes CONTENT, not reachability: a definition already sealed under
// one parent may still be included under another, because including it does not
// write to it. Writing to it is what is refused.
func TestFreeze_MutateAfterBuildPanics_IncludeAfterBuildSucceeds(t *testing.T) {
	child := billingApp()
	first := quiet("first")
	first.Use(child)
	if err := build(first); err != nil {
		t.Fatalf("build: %v", err)
	}

	// Mutate-after-seal: refused, and the message says who sealed and where.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("a frozen App accepted a new entry")
			}
			msg, _ := r.(string)
			for _, want := range []string{"frozen", "billing", "compose_test.go:"} {
				if !strings.Contains(msg, want) {
					t.Errorf("panic does not mention %q: %s", want, msg)
				}
			}
		}()
		child.Get("/sneak", func(c *Ctx) error { return nil })
	}()

	// Proxy-after-seal: allowed. A second host includes the same sealed
	// definition, and gets its own occurrence of it.
	second := quiet("second")
	second.Group("/v2").Use(child)
	if err := build(second); err != nil {
		t.Fatalf("including a frozen definition was refused: %v", err)
	}
	if n := len(second.Registry()); n != 1 {
		t.Fatalf("the second host got %d ops from the frozen definition, want 1", n)
	}
	if got := second.Registry()[0].Path; got != "/v2/invoices/:id" {
		t.Errorf("second host path = %q, want /v2/invoices/:id", got)
	}
}

// TestFreeze_ReadingDoesNotFreeze. A codegen step, a test or a doc generator that
// inspects the program must not turn the next legitimate Use into a panic about
// a seal nobody asked for.
func TestFreeze_ReadingDoesNotFreeze(t *testing.T) {
	root := quiet("root")
	root.Use(billingApp())

	_ = root.Registry()
	_ = root.OpenAPISpec()
	_ = root.Declaration()
	_ = root.Fiber()
	_ = root.Lint()

	if root.Frozen() {
		t.Fatal("inspecting the program froze it")
	}
	root.Use(H(func(c *Ctx) error { return c.Continue() })) // must not panic
}

// ── conflicts ───────────────────────────────────────────────────────────────

// TestConflicts_AllOfThemAtOnceWithBothPartiesNamed. Failing fast reports one
// collision per build, so three disagreements take three builds to learn. And
// only a walk over the whole set can name BOTH parties: eager composition could
// attribute a collision to whichever app composed second, which is wiring-file
// line order and means nothing.
func TestConflicts_AllOfThemAtOnceWithBothPartiesNamed(t *testing.T) {
	iam := quiet("iam")
	iam.Get("/users", func(c *Ctx) error { return nil })
	iam.Get("/tokens", func(c *Ctx) error { return nil })

	billing := quiet("billing")
	billing.Get("/users", func(c *Ctx) error { return nil })  // collides
	billing.Get("/tokens", func(c *Ctx) error { return nil }) // collides too

	root := quiet("root")
	root.Use(iam)
	root.Use(billing)

	err := build(root)
	if err == nil {
		t.Fatal("two apps claiming two addresses each were accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"GET /users", "GET /tokens", `"iam"`, `"billing"`, "compose_test.go:",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict report does not mention %q:\n%s", want, msg)
		}
	}
	// BOTH, in one report — not the first and then a second build.
	if strings.Count(msg, "declared by") != 2 {
		t.Errorf("want 2 conflicts reported together, got:\n%s", msg)
	}
}

// TestConflicts_NamesTheCompositionPathNotJustTheApp. "declared by billing" is
// not enough when billing is included twice; the breadcrumb is what says which
// inclusion.
func TestConflicts_NamesTheCompositionPath(t *testing.T) {
	leaf := quiet("leaf")
	leaf.Get("/x", func(c *Ctx) error { return nil })

	root := quiet("root")
	root.Group("/v1").Use(leaf)
	root.Group("/v1").Use(leaf) // same prefix twice: the same address twice

	err := build(root)
	if err == nil {
		t.Fatal("one address claimed twice was accepted")
	}
	if !strings.Contains(err.Error(), "root → /v1 → leaf") {
		t.Fatalf("conflict does not carry the composition path: %v", err)
	}
}

// ── Use position: a Handler may never terminate a request ───────────────────

// TestUse_ATerminalHandlerIsRefusedOnTheAppBeingServed.
//
// This is the rule the three node kinds encode — Use is for handlers that WRAP,
// an address is something only a route method can say — and until the walk
// could SEE terminality, nothing enforced it. The inert check enforces something
// strictly weaker (a definition carrying middleware must have routes beneath
// it), which app.Use(zip.Static(assets)) passes on any app that has a route at
// all. It would then answer every one of them.
//
// Depth 0 is the sharp case: the app being SERVED is where a static handler gets
// composed in real wiring files, and it was skipped entirely.
func TestUse_ATerminalHandlerIsRefusedOnTheAppBeingServed(t *testing.T) {
	assets := fstest.MapFS{"main.css": &fstest.MapFile{Data: []byte("body{}")}}

	root := quiet("root")
	root.Get("/x", func(c *Ctx) error { return nil })
	root.Use(Static(assets)) // depth 0 — the app being served

	err := build(root)
	if err == nil {
		t.Fatal("a terminal handler was composed with Use and the composition was accepted — " +
			"it answers every request beneath it, so nothing composed after it is ever reached")
	}
	for _, want := range []string{"zip.Static", `"root"`, "compose_test.go:", "ANSWERS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}

	// The blessed spelling is untouched: registered AT an address, it serves.
	fine := quiet("fine")
	fine.Get("/assets/*", Static(assets))
	if err := build(fine); err != nil {
		t.Fatalf("a leaf registered at its address was refused: %v", err)
	}
	if code, body := wireGET(t, fine, "/assets/main.css"); code != 200 || body != "body{}" {
		t.Errorf("GET /assets/main.css = %d %q, want 200 %q", code, body, "body{}")
	}
}

// TestUse_ATerminalHandlerIsRefusedInsideAnIncludedDefinition: the same rule one
// level down, where the definition that wrote the line is not the one being
// served — so the message has to name the definition AND the composition trail
// to be actionable at all.
func TestUse_ATerminalHandlerIsRefusedInsideAnIncludedDefinition(t *testing.T) {
	ui := quiet("ui")
	ui.Use(Static(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>")}}))
	ui.Get("/health", func(c *Ctx) error { return nil }) // routes, so it is not merely inert

	root := quiet("root")
	root.Group("/app").Use(ui)

	err := build(root)
	if err == nil {
		t.Fatal("a terminal handler inside an included definition was accepted")
	}
	for _, want := range []string{"zip.Static", `"ui"`, "root → /app → ui", "compose_test.go:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
}

// TestUse_TerminalityIsADeclaredPropertyNotAName is the mechanism, pinned.
//
// A name match would bless zip's own three leaves and miss every leaf anyone
// else writes — the wrong half of the fleet. So a constructor DECLARES what it
// built, through [Terminal], and the two handlers below are the proof that the
// declaration and not the shape is what decides: AdaptNetHTTP and
// AdaptNetHTTPMiddleware return closures with the same body, over the same
// adapter type, from the same file. One answers, one wraps. Only their authors
// could know which, and only one of them is refused.
func TestUse_TerminalityIsADeclaredPropertyNotAName(t *testing.T) {
	wraps := quiet("wraps")
	wraps.Get("/x", func(c *Ctx) error { return nil })
	wraps.Use(AdaptNetHTTPMiddleware(func(next http.Handler) http.Handler { return next }))
	if err := build(wraps); err != nil {
		t.Fatalf("a net/http MIDDLEWARE adapter was refused in Use position: %v", err)
	}

	answers := quiet("answers")
	answers.Get("/x", func(c *Ctx) error { return nil })
	answers.Use(AdaptNetHTTP(http.NotFoundHandler()))
	err := build(answers)
	if err == nil {
		t.Fatal("a net/http HANDLER adapter was accepted in Use position; it answers every request")
	}
	if !strings.Contains(err.Error(), "zip.AdaptNetHTTP") {
		t.Errorf("refusal does not name the constructor: %v", err)
	}

	// And it is not zip's own three: anyone's leaf constructor says so the same
	// way, which is why Terminal is exported. (wsx.Upgrade is one of these.)
	mine := quiet("mine")
	mine.Get("/x", func(c *Ctx) error { return nil })
	mine.Use(Terminal("spa.Shell", func(c *Ctx) error { return c.String(200, "<html>") }))
	err = build(mine)
	if err == nil {
		t.Fatal("a third-party terminal handler was accepted in Use position")
	}
	if !strings.Contains(err.Error(), "spa.Shell") {
		t.Errorf("refusal does not name the third-party constructor: %v", err)
	}

	// The SAME closure, undeclared, is ordinary middleware and stays legal —
	// terminality is what the constructor said, never what the handler looks
	// like or where it came from.
	plain := quiet("plain")
	plain.Get("/x", func(c *Ctx) error { return nil })
	plain.Use(H(func(c *Ctx) error { return c.String(200, "<html>") }))
	if err := build(plain); err != nil {
		t.Fatalf("an undeclared handler was refused in Use position: %v", err)
	}
}

// TestInert_TheServedAppsMiddlewareIsGlobalAndSoIsNeverInert documents the one
// thing the depth-0 skip in the inert check guards, because the terminal rule
// above deliberately does NOT share it.
//
// Middleware on the app being served is registered as ROUTER middleware
// (App.materialise), so it runs for every request the process answers —
// including requests that match no route at all, which is what 404 logging, CORS
// preflight and recovery exist for. A root that declares a logger and gets its
// routes from the definitions it composes has nothing wrong with it, and "no
// routes beneath it" is not evidence of anything there. That is the whole of the
// exemption: it is about COVERAGE, and it says nothing about whether a handler
// wraps — which is why the terminal check asks its own question, at depth 0 too.
func TestInert_TheServedAppsMiddlewareIsGlobalAndSoIsNeverInert(t *testing.T) {
	ran := 0
	root := quiet("root") // no routes of its own, ever
	root.Use(H(func(c *Ctx) error { ran++; return c.Continue() }))
	child := quiet("child")
	child.Get("/x", func(c *Ctx) error { return c.String(200, "ok") })
	root.Use(child)

	if err := build(root); err != nil {
		t.Fatalf("the served app's own middleware was called inert: %v", err)
	}
	if code, _ := wireGET(t, root, "/nowhere"); code != 404 {
		t.Fatalf("GET /nowhere = %d, want 404", code)
	}
	if ran == 0 {
		t.Error("root middleware did not run for an unmatched request — if that were true, " +
			"the depth-0 exemption would be wrong and this middleware would be inert after all")
	}
}

// ── concurrency ─────────────────────────────────────────────────────────────

// TestRegistry_RaceCleanOnALiveGeneration. Registry is read from serving goroutines —
// the OpenAPI endpoint and the MCP tool listing both call it per request — so
// it has to be safe without a mutex on the served path. Sealing is what buys
// that: the program cannot change, so the projection is computed once under
// sync.Once and shared.
func TestRegistry_RaceCleanOnALiveGeneration(t *testing.T) {
	root := quiet("root")
	root.Group("/v1").Use(unnamedApp())
	root.Group("/admin").Use(unnamedApp())
	if err := build(root); err != nil {
		t.Fatalf("build: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n := len(root.Registry()); n != 2 {
				t.Errorf("Registry() = %d ops, want 2", n)
			}
			_ = root.OpenAPISpec()
			_ = root.Commands()
		}()
	}
	wg.Wait()
}

// TestRegistry_IsComputedOncePerGeneration: the same slice, not an equal one. If it
// were rebuilt per call, every OpenAPI request would re-walk the graph.
func TestRegistry_IsComputedOncePerGeneration(t *testing.T) {
	root := quiet("root")
	root.Use(billingApp())
	if err := build(root); err != nil {
		t.Fatalf("build: %v", err)
	}
	first := root.Registry()
	if &first[0] != &root.Registry()[0] {
		t.Fatal("Registry() rebuilt within a generation; it must be computed once")
	}
}

// ── the H footnote ──────────────────────────────────────────────────────────

// TestH_IsNeededOnlyForABareClosureAtUse pins the migration claim in the type
// system rather than in prose. Everything already typed as a Handler composes
// with no wrapper; every route method still takes ...Handler so an inline
// closure there is untouched; the ONE construct that needs H is a bare closure
// written at a Use call, and that is a compile-time fact, not a style note.
func TestH_IsNeededOnlyForABareClosureAtUse(t *testing.T) {
	app := quiet("app")

	// 1. A value already typed Handler is a Component. No wrapper.
	var typed Handler = func(c *Ctx) error { return c.Continue() }
	app.Use(typed)

	// 2. A constructor that returns Handler — every middleware in the package —
	//    is a Component. No wrapper.
	app.Use(recoverish())

	// 3. Route methods still take ...Handler, so an inline closure is fine.
	app.Get("/x", func(c *Ctx) error { return nil })
	app.With(func(next Handler) Handler { return next }).Post("/y", func(c *Ctx) error { return nil })

	// 4. Only the bare closure at Use needs it. The commented line below is the
	//    compile error this test exists to describe:
	//        app.Use(func(c *Ctx) error { return c.Continue() })
	//        → func(*Ctx) error does not implement Component (missing method component)
	app.Use(H(func(c *Ctx) error { return c.Continue() }))

	if n := len(app.entries); n != 5 {
		t.Fatalf("got %d entries, want 5", n)
	}
}

func recoverish() Handler { return func(c *Ctx) error { return c.Continue() } }

// ── the union ───────────────────────────────────────────────────────────────

// TestCompose_DocumentIsTheUnion is the property behind "Graft took iam from 4
// paths to 164": composing is what makes a host's document the whole surface
// rather than the wildcard it hung a closure on. Under AdaptNetHTTP the child
// went in as an http.Handler and the count stayed at the host's own.
//
// The number is arithmetic here, not a fixture, so it pins the RULE.
func TestCompose_DocumentIsTheUnion(t *testing.T) {
	const own, childOps = 4, 160

	child := quiet("iam")
	for i := 0; i < childOps; i++ {
		Get(child, "/v1/iam/r"+itoa(i), func(_ context.Context, in *invoiceIn) (*invoiceOut, error) {
			return &invoiceOut{}, nil
		}, WithOperationID("iam_r"+itoa(i)))
	}
	host := quiet("host")
	for i := 0; i < own; i++ {
		Get(host, "/v1/host/r"+itoa(i), func(_ context.Context, in *invoiceIn) (*invoiceOut, error) {
			return &invoiceOut{}, nil
		}, WithOperationID("host_r"+itoa(i)))
	}
	if n := len(host.OpenAPISpec()["paths"].(map[string]map[string]any)); n != own {
		t.Fatalf("the host alone has %d paths, want %d", n, own)
	}

	host.Use(child)
	if err := build(host); err != nil {
		t.Fatalf("build: %v", err)
	}
	if n := len(host.OpenAPISpec()["paths"].(map[string]map[string]any)); n != own+childOps {
		t.Fatalf("composed document has %d paths, want %d", n, own+childOps)
	}
	// And every other projection agrees, from the same registry.
	if n := len(host.Registry()); n != own+childOps {
		t.Errorf("registry has %d ops, want %d", n, own+childOps)
	}
	if n := len(host.Commands()); n != own+childOps {
		t.Errorf("CLI has %d commands, want %d", n, own+childOps)
	}
	if n := len(host.Declaration().Routes); n != own+childOps {
		t.Errorf("declaration has %d routes, want %d", n, own+childOps)
	}
}

// TestCompose_ADefinitionsOwnDocumentIsUnchanged. Being included must not edit
// what a definition says about itself: a service served standalone describes
// its types under their plain names and its ops under the ids it declared,
// whether or not some host somewhere composed it.
func TestCompose_ADefinitionsOwnDocumentIsUnchanged(t *testing.T) {
	solo := billingApp()
	before := jsonOf(t, solo.OpenAPISpec())

	host := quiet("host")
	host.Group("/v1").Use(solo)
	_ = host.OpenAPISpec() // the composed document is rendered

	if after := jsonOf(t, solo.OpenAPISpec()); after != before {
		t.Fatalf("being composed edited the definition's own document:\n before %s\n after  %s", before, after)
	}
	// Concretely: its op is still listInvoices at /invoices/{id} — and so is
	// the HOST's occurrence of it. A DECLARED id is a published name and
	// composition does not get a vote (see [occurrenceID]); only the ADDRESS is
	// the host's to compose, and /v1/invoices/{id} is where it now answers.
	if id := solo.Registry()[0].OperationID; id != "listInvoices" {
		t.Errorf("the definition's own op id became %q", id)
	}
	if id := host.Registry()[0].OperationID; id != "listInvoices" {
		t.Errorf("the host's occurrence id = %q, want listInvoices — a declared id "+
			"survives composition verbatim", id)
	}
	if p := host.Registry()[0].Path; p != "/v1/invoices/:id" {
		t.Errorf("the host's occurrence path = %q, want /v1/invoices/:id", p)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func itoa(i int) string { return strconv.Itoa(i) }

// hostOf returns the Host running a, building generation 0 if it has none.
// Include and Drop are HOST verbs — a program extends with Use, a running one
// publishes a new generation — so a test that changes a live composition needs
// the host, not the App.
func hostOf(t *testing.T, a *App) *Host {
	t.Helper()
	if _, live := a.Generation(); live {
		return &Host{app: a}
	}
	h, err := host(a)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	return h
}
