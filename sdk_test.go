package zip_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// ---------------------------------------------------------------------------
// One service, registered the ordinary way. Everything the generated package
// contains is read back off these declarations — nothing about the SDK is
// written down anywhere, which is the property under test.
// ---------------------------------------------------------------------------

// Height is a chain's last accepted block height.
type Height struct {
	// Height is the height itself.
	Height uint64 `json:"height"`
}

// ValidatorsIn asks for one network's validators.
type ValidatorsIn struct {
	// NetID names the network whose validators are wanted.
	NetID string `json:"netID" validate:"required"`
	// NodeIDs narrows the answer to these nodes.
	NodeIDs []string `json:"nodeIDs"`
}

type Validator struct {
	NodeID string `json:"nodeID"`
	Weight uint64 `json:"weight"`
	Fee    int32  `json:"fee"`
}

type Validators struct {
	Validators []Validator `json:"validators"`
}

func sdkGetHeight(context.Context, *struct{}) (*Height, error) {
	return &Height{Height: 0xDEADBEEFCAFE}, nil
}

// sdkGetValidators echoes its whole input back through the reply, so a field
// read from the wrong offset lands visibly rather than as a plausible zero.
func sdkGetValidators(_ context.Context, in *ValidatorsIn) (*Validators, error) {
	out := &Validators{}
	for i, n := range in.NodeIDs {
		out.Validators = append(out.Validators, Validator{
			NodeID: in.NetID + "/" + n,
			Weight: uint64(len(n)),
			Fee:    int32(-1 - i),
		})
	}
	return out, nil
}

func sdkApp() *zip.App {
	app := zip.New(zip.Config{AppName: "platform", DisableStartupMessage: true})
	zip.Get(app, "/v1/platform/height", sdkGetHeight)
	zip.Post(app, "/v1/platform/validators", sdkGetValidators)
	return app
}

// TestSDK_CallsTheLiveServiceOverZAP is the headline: a Go package that never
// saw the service's types, generated from its document alone, calls it over ZAP
// and gets the exact values back — including a nested list of structs and a
// signed field beside an unsigned one, where a mistaken width would move every
// field after it.
//
// It compiles the generated package for real, because "it renders plausible Go"
// is not the claim. The claim is that it CALLS.
func TestSDK_CallsTheLiveServiceOverZAP(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a package with the go toolchain")
	}
	sdk, err := sdkApp().SDK("platform")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if len(sdk.Gaps) != 0 {
		t.Fatalf("gaps on a document with none: %+v", sdk.Gaps)
	}

	// The probe is built OFFLINE against this checkout: zip's own go.sum
	// resolves its dependency graph from the module cache, so the test needs no
	// network and no `go mod tidy`.
	dir := t.TempDir()
	zipDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	goLine, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	directive := "go 1.26.5"
	for _, line := range strings.Split(string(goLine), "\n") {
		if strings.HasPrefix(line, "go ") {
			directive = strings.TrimSpace(line)
			break
		}
	}
	write := func(name string, body []byte) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module sdkprobe\n\n"+directive+
		"\n\nrequire github.com/zap-proto/zip v1.36.16\n\nreplace github.com/zap-proto/zip => "+zipDir+"\n"))
	write("go.sum", sum)
	write("platform/client.go", sdk.Source)
	write("main.go", []byte(sdkProbeMain))

	// The driver serves the SAME handlers over a unix socket and calls them
	// through the generated client, so the two ends of the assertion are the
	// declaration and the generated package, with the wire in between.
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "missing go.sum entry") || strings.Contains(string(out), "cannot find module") {
			t.Skipf("module cache does not hold zip's dependencies: %s", out)
		}
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ROUNDTRIP OK") {
		t.Fatalf("generated client did not round-trip:\n%s", out)
	}
}

// sdkProbeMain drives the generated package against the live app.
const sdkProbeMain = `package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zap-proto/zip"
	"sdkprobe/platform"
)

type height struct {
	Height uint64 ` + "`json:\"height\"`" + `
}
type validatorsIn struct {
	NetID   string   ` + "`json:\"netID\" validate:\"required\"`" + `
	NodeIDs []string ` + "`json:\"nodeIDs\"`" + `
}
type validator struct {
	NodeID string ` + "`json:\"nodeID\"`" + `
	Weight uint64 ` + "`json:\"weight\"`" + `
	Fee    int32  ` + "`json:\"fee\"`" + `
}
type validators struct {
	Validators []validator ` + "`json:\"validators\"`" + `
}

func getHeight(context.Context, *struct{}) (*height, error) {
	return &height{Height: 0xDEADBEEFCAFE}, nil
}
func getValidators(_ context.Context, in *validatorsIn) (*validators, error) {
	out := &validators{}
	for i, n := range in.NodeIDs {
		out.Validators = append(out.Validators, validator{
			NodeID: in.NetID + "/" + n, Weight: uint64(len(n)), Fee: int32(-1 - i),
		})
	}
	return out, nil
}

func fail(what string, args ...any) {
	fmt.Fprintf(os.Stderr, what+"\n", args...)
	os.Exit(1)
}

func main() {
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("sdkprobe-%d.sock", os.Getpid()))
	defer os.Remove(sock)

	app := zip.New(zip.Config{AppName: "platform", DisableStartupMessage: true})
	zip.Get(app, "/v1/platform/height", getHeight)
	zip.Post(app, "/v1/platform/validators", getValidators)
	if _, err := zip.Serve(app, sock); err != nil {
		fail("serve: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	c, err := platform.Dial(sock)
	if err != nil {
		fail("dial: %v", err)
	}
	ctx := context.Background()

	h, err := c.GetPlatformHeight(ctx)
	if err != nil {
		fail("GetPlatformHeight: %v", err)
	}
	if h.Height != 0xDEADBEEFCAFE {
		fail("height: got %#x want 0xdeadbeefcafe", h.Height)
	}

	v, err := c.PostPlatformValidators(ctx, &platform.ValidatorsIn{
		NetID: "8675309", NodeIDs: []string{"alpha", "bee"},
	})
	if err != nil {
		fail("PostPlatformValidators: %v", err)
	}
	want := []platform.Validator{
		{NodeID: "8675309/alpha", Weight: 5, Fee: -1},
		{NodeID: "8675309/bee", Weight: 3, Fee: -2},
	}
	if len(v.Validators) != len(want) {
		fail("validators: got %d want %d (%+v)", len(v.Validators), len(want), v.Validators)
	}
	for i := range want {
		if v.Validators[i] != want[i] {
			fail("validator %d: got %+v want %+v", i, v.Validators[i], want[i])
		}
	}
	fmt.Println("ROUNDTRIP OK")
}
`

// TestSDK_TheNameIsTheOpsName is what makes the whole exercise worth doing: a
// hand-written client sends a method string somebody typed, and a typo compiles.
// Here every call site is the operationId the registry published, so the class
// of defect where a client addresses a name the service does not serve cannot
// be written down.
func TestSDK_TheNameIsTheOpsName(t *testing.T) {
	app := sdkApp()
	sdk, err := app.SDK("platform")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	src := string(sdk.Source)

	ids := map[string]bool{}
	for _, c := range app.Commands() {
		ids[c.OperationID] = true
	}
	if len(ids) == 0 {
		t.Fatal("no ops registered")
	}
	for id := range ids {
		if !strings.Contains(src, `"`+id+`"`) {
			t.Errorf("op %q has no call site in the generated client", id)
		}
	}
	// And nothing else is addressed. Every quoted argument to Call must be an
	// id the service actually published.
	for _, line := range strings.Split(src, "\n") {
		i := strings.Index(line, "c.conn, \"")
		if i < 0 {
			continue
		}
		rest := line[i+len("c.conn, \""):]
		called := rest[:strings.IndexByte(rest, '"')]
		if !ids[called] {
			t.Errorf("generated client calls %q, which no op declares", called)
		}
	}
}

// TestSDK_IsDeterministic pins that regenerating an unchanged registry produces
// an unchanged file. A generator whose output depends on map order makes every
// regeneration a diff, and a diff nobody can read is a diff nobody reviews.
func TestSDK_IsDeterministic(t *testing.T) {
	first, err := sdkApp().SDK("platform")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	for i := 0; i < 8; i++ {
		again, err := sdkApp().SDK("platform")
		if err != nil {
			t.Fatalf("SDK: %v", err)
		}
		if string(again.Source) != string(first.Source) {
			t.Fatalf("run %d differs from run 0", i+1)
		}
	}
}

// TestSDK_TheLayoutIsTheSameLayout is the property the whole derivation rests
// on: the generated struct and the declared struct have byte-identical wire
// shapes. A field order, a skipped tag or a flattened embedding that diverged
// would not fail to compile — it would read every value after it from the wrong
// offset, which is what TestSDK_CallsTheLiveServiceOverZAP catches at run time
// and this catches at the derivation.
func TestSDK_TheLayoutIsTheSameLayout(t *testing.T) {
	sdk, err := sdkApp().SDK("platform")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	src := string(sdk.Source)
	// Declaration order, verbatim from the Go type — not the document's
	// alphabetical property list, where Fee would come first.
	want := "type Validator struct {\n\tNodeID string `json:\"nodeID\"`\n\tWeight uint64 `json:\"weight\"`\n\tFee int32 `json:\"fee\"`\n}"
	got := strings.Join(strings.Fields(src), " ")
	if !strings.Contains(got, strings.Join(strings.Fields(want), " ")) {
		t.Fatalf("Validator is not declared field-for-field in order:\n%s", src)
	}
}

// TestSDK_WidthsAreExact is why the derivation reads the Go type. Over ZAP a
// field is an offset AND a width, so a generated uint32 read as int64 does not
// merely mis-print: it takes eight bytes where the service laid four, and every
// field after it is read from the wrong place.
func TestSDK_WidthsAreExact(t *testing.T) {
	type widths struct {
		A uint8   `json:"a"`
		B int16   `json:"b"`
		C uint32  `json:"c"`
		D int64   `json:"d"`
		E float32 `json:"e"`
		F float64 `json:"f"`
	}
	app := zip.New(zip.Config{AppName: "w", DisableStartupMessage: true})
	zip.Post(app, "/v1/w", func(_ context.Context, in *widths) (*widths, error) { return in, nil })

	sdk, err := app.SDK("w")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	// gofmt aligns a struct's columns, so the assertion reads the fields with
	// their runs of spaces collapsed rather than pinning the alignment.
	src := strings.Join(strings.Fields(string(sdk.Source)), " ")
	for _, want := range []string{
		"A uint8 `json:\"a\"`",
		"B int16 `json:\"b\"`",
		"C uint32 `json:\"c\"`",
		"D int64 `json:\"d\"`",
		"E float32 `json:\"e\"`",
		"F float64 `json:\"f\"`",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated package does not declare %s\n--- source ---\n%s", want, sdk.Source)
		}
	}
}

// TestSDK_GapsAreNotSilent is the same rule [Schema.Gaps] follows: a value the
// wire cannot carry is REPORTED, never quietly dropped. An op whose input holds
// one gets no method at all — a client that could be called and could never
// succeed is worse than one that says the operation is out of reach.
func TestSDK_GapsAreNotSilent(t *testing.T) {
	type ledger struct {
		Fine    string         `json:"fine"`
		Balance map[string]int `json:"balance"`
	}
	type loose struct {
		Fine  string `json:"fine"`
		Extra any    `json:"extra"`
	}
	for _, tc := range []struct {
		name  string
		reg   func(*zip.App)
		cause string
	}{
		{"map", func(a *zip.App) {
			zip.Post(a, "/v1/a", func(_ context.Context, in *ledger) (*ledger, error) { return in, nil })
		}, zip.CauseMap},
		{"any", func(a *zip.App) {
			zip.Post(a, "/v1/a", func(_ context.Context, in *loose) (*loose, error) { return in, nil })
		}, zip.CauseAny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := zip.New(zip.Config{AppName: "a", DisableStartupMessage: true})
			tc.reg(app)
			sdk, err := app.SDK("a")
			if err != nil {
				t.Fatalf("SDK: %v", err)
			}
			if sdk.Ops() != 0 {
				t.Errorf("an op the wire refuses got %d method(s):\n%s", sdk.Ops(), sdk.Source)
			}
			if len(sdk.Gaps) == 0 {
				t.Fatal("no gap reported")
			}
			for _, g := range sdk.Gaps {
				if g.Cause == tc.cause {
					return
				}
			}
			t.Errorf("gaps do not name %q: %+v", tc.cause, sdk.Gaps)
		})
	}
}

// TestSDK_AWholeAppStillGeneratesAroundAGap pins that one unreachable op does
// not cost the others their methods. The refusal is per operation, because that
// is the granularity at which it is true.
func TestSDK_AWholeAppStillGeneratesAroundAGap(t *testing.T) {
	type ledger struct {
		Balance map[string]int `json:"balance"`
	}
	app := sdkApp()
	zip.Post(app, "/v1/platform/ledger", func(_ context.Context, in *ledger) (*ledger, error) { return in, nil })

	sdk, err := app.SDK("platform")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if sdk.Ops() != 2 {
		t.Errorf("got %d methods, want the 2 reachable ops:\n%s", sdk.Ops(), sdk.Source)
	}
	if len(sdk.Gaps) != 1 {
		t.Errorf("got %d gaps, want 1: %+v", len(sdk.Gaps), sdk.Gaps)
	}
}

// TestSDK_AnIdNeedsItsCodecFirst is the gap that covers most of a chain node's
// API, so it is worth naming: ids.ID is [32]byte and ids.NodeID is [20]byte, and
// a fixed array is exactly the case where the LAYOUT is right and the reflective
// encoder still refuses — the type is expected to declare its own wire.
//
// Without this the generated client offers the method and the call fails on the
// wire, which is the failure this projection exists to remove. It is also not
// fixable by generating harder: the declared type's MarshalZAP lives in the
// service's package, and restating the field as [32]uint8 restates the bytes and
// not the codec.
func TestSDK_AnIdNeedsItsCodecFirst(t *testing.T) {
	type ID [32]byte
	type Tx struct {
		TxID   ID     `json:"txID"`
		Height uint64 `json:"height"`
	}
	app := zip.New(zip.Config{AppName: "p", DisableStartupMessage: true})
	zip.Get(app, "/v1/p/tx", func(context.Context, *struct{}) (*Tx, error) { return &Tx{}, nil })

	sdk, err := app.SDK("p")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if sdk.Ops() != 0 {
		t.Errorf("an op carrying an id got a method that cannot succeed:\n%s", sdk.Source)
	}
	if len(sdk.Gaps) != 1 || sdk.Gaps[0].Cause != "no codec" {
		t.Fatalf("gaps do not name the missing codec: %+v", sdk.Gaps)
	}
	if !strings.Contains(sdk.Gaps[0].Go, "bytes_fixed[32]") {
		t.Errorf("the gap does not say what the field is: %+v", sdk.Gaps[0])
	}
	// The ZAP schema says the same thing about the same field, from the same
	// layout — the two projections agree on what is out of reach.
	if coded := zip.ZAPSchema("p", app).Coded; len(coded) != 1 || coded[0].Field != "TxID" {
		t.Errorf("the schema and the SDK disagree about the field: %+v", coded)
	}
}

// TestSDK_OneOperationOneMethodName pins the naming rule across the spellings an
// operationId actually takes in the fleet, so a package regenerated after an id
// is respelled the same way twice is not a rename.
func TestSDK_OneOperationOneMethodName(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"get_platform_height", "GetPlatformHeight"},
		{"platform.getHeight", "PlatformGetHeight"},
		{"platform-get-height", "PlatformGetHeight"},
		{"getHeight", "GetHeight"},
		{"nodeID", "NodeID"},
	} {
		app := zip.New(zip.Config{AppName: "n", DisableStartupMessage: true})
		zip.Get(app, "/v1/n", sdkGetHeight, zip.WithOperationID(tc.id))
		sdk, err := app.SDK("n")
		if err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
		if !strings.Contains(string(sdk.Source), "func (c *Client) "+tc.want+"(") {
			t.Errorf("%q rendered no method %s\n%s", tc.id, tc.want, sdk.Source)
		}
	}
}

// An id is a fixed array, which the layout states and reflection refuses. When
// one sits on an EMBEDDED field the substitute this used to write —
// "struct{} `json:\"...\"`" — is not Go: an embedded field must be a name. The
// package did not parse, App.SDK returned an error, and the app got no client
// at all. Found generating node's xvm service, where one op of ten has it.
func TestSDK_AnEmbeddedUnnameableTypeIsAGapAndNotASyntaxError(t *testing.T) {
	// Exported, because an embedded unexported type is on no wire at all: the
	// renderer and the layout both walk exported fields only, and skip it
	// together. node's is FormattedAssetID.
	type AssetID struct {
		ID [32]byte `json:"id"`
	}
	type asset struct {
		AssetID `json:"assetID"`
		Name    string `json:"name"`
	}
	app := zip.New(zip.Config{AppName: "x", DisableStartupMessage: true})
	zip.Get(app, "/v1/asset", func(_ context.Context, in *asset) (*asset, error) { return in, nil },
		zip.WithOperationID("x_asset"))
	zip.Get(app, "/v1/height", func(_ context.Context, in *Height) (*Height, error) { return in, nil },
		zip.WithOperationID("x_height"))

	sdk, err := app.SDK("x")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if strings.Contains(string(sdk.Source), "struct{} `json") {
		t.Errorf("an embedded field was written as a type with no name:\n%s", sdk.Source)
	}
	if !strings.Contains(string(sdk.Source), "func (c *Client) XHeight") {
		t.Error("the op that CAN cross lost its method to the one that cannot")
	}
	if strings.Contains(string(sdk.Source), "XAsset") {
		t.Error("the op the wire refuses got a method")
	}
	if len(sdk.Gaps) == 0 {
		t.Fatal("no gap reported for the refused op")
	}
}

// A field whose own type cannot cross used to be replaced by an empty struct,
// and the op kept its method: the call succeeded and the value was gone.
// That is the "compiles and lies" outcome this projection exists to prevent,
// and it reached real clients — node's admin, info and platformvm all had
// []struct{} where a payload should be.
func TestSDK_ANestedRefusalRefusesTheOp(t *testing.T) {
	type inner struct {
		ID [32]byte `json:"id"`
	}
	type outer struct {
		Name  string  `json:"name"`
		Items []inner `json:"items"`
	}
	app := zip.New(zip.Config{AppName: "n", DisableStartupMessage: true})
	zip.Post(app, "/v1/outer", func(_ context.Context, in *outer) (*outer, error) { return in, nil },
		zip.WithOperationID("n_outer"))

	sdk, err := app.SDK("n")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if sdk.Ops() != 0 {
		t.Errorf("an op whose payload cannot cross got %d method(s):\n%s", sdk.Ops(), sdk.Source)
	}
	if strings.Contains(string(sdk.Source), "[]struct{}") {
		t.Errorf("a field that cannot cross was written as an empty struct:\n%s", sdk.Source)
	}
	if len(sdk.Gaps) == 0 {
		t.Fatal("no gap reported")
	}
}

// Two ops sharing one refused type are two ops with no method, so they are two
// gaps. The memo that remembers the refusal used to answer "no" and say
// nothing, so the second op vanished from the package with nothing recorded and
// the only way to notice was to count the methods. node's platformvm had
// get_tx_rewards disappear exactly this way.
func TestSDK_EveryOpRefusedIsAnOpReported(t *testing.T) {
	type shared struct {
		ID [32]byte `json:"id"`
	}
	app := zip.New(zip.Config{AppName: "s", DisableStartupMessage: true})
	zip.Get(app, "/v1/one", func(_ context.Context, in *shared) (*shared, error) { return in, nil },
		zip.WithOperationID("s_one"))
	zip.Get(app, "/v1/two", func(_ context.Context, in *shared) (*shared, error) { return in, nil },
		zip.WithOperationID("s_two"))

	sdk, err := app.SDK("s")
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if sdk.Ops() != 0 {
		t.Fatalf("got %d method(s), want none:\n%s", sdk.Ops(), sdk.Source)
	}
	named := map[string]bool{}
	for _, g := range sdk.Gaps {
		named[g.Op] = true
	}
	for _, op := range []string{"s_one", "s_two"} {
		if !named[op] {
			t.Errorf("%s has no method and no gap; gaps are %+v", op, sdk.Gaps)
		}
	}
}
