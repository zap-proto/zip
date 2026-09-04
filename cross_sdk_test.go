package zip_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

type AccountIn struct {
	ID string `json:"id" validate:"required"`
}

type AccountOut struct {
	ID      string `json:"id"`
	Balance uint64 `json:"balance"`
	Active  bool   `json:"active"`
}

func getAccount(context.Context, *AccountIn) (*AccountOut, error) {
	return &AccountOut{ID: "acc_1", Balance: 1000, Active: true}, nil
}

func crossApp() *zip.App {
	app := zip.New(zip.Config{AppName: "ledger", DisableStartupMessage: true})
	zip.Describe("POST /v1/account", zip.Doc{
		Description: "GetAccount retrieves account details by ID.\n\nReturns the balance and active status.",
		Fields: map[string]string{
			"AccountIn.id":        "Unique account identifier",
			"AccountOut.id":       "Account identifier",
			"AccountOut.balance":  "Current balance in cents",
			"AccountOut.active":   "Whether account is currently active",
		},
		Example:  []byte(`{"id": "acc_1"}`),
		Response: []byte(`{"id": "acc_1", "balance": 1000, "active": true}`),
	})
	zip.Post(app, "/v1/account", getAccount)
	return app
}

func TestRustSDK_GeneratesSelfDocumentingSDK(t *testing.T) {
	app := crossApp()
	sdk, err := app.RustSDK("ledger")
	if err != nil {
		t.Fatalf("RustSDK error: %v", err)
	}
	src := string(sdk.Source)
	t.Logf("Generated Rust SDK Source:\n%s", src)

	// Check self-documenting rustdoc comments
	if !strings.Contains(src, "/// GetAccount retrieves account details by ID.") {
		t.Errorf("Rust SDK missing method rustdoc comment")
	}
	if !strings.Contains(src, "/// # Example") {
		t.Errorf("Rust SDK missing example block")
	}
	if !strings.Contains(src, "pub struct AccountIn") {
		t.Errorf("Rust SDK missing AccountIn struct")
	}
	if !strings.Contains(src, "pub struct AccountOut") {
		t.Errorf("Rust SDK missing AccountOut struct")
	}
	if !strings.Contains(src, "pub id: String,") {
		t.Errorf("Rust SDK missing id field")
	}
	if !strings.Contains(src, "pub balance: u64,") {
		t.Errorf("Rust SDK missing balance field")
	}
	if !strings.Contains(src, "pub async fn post_account") {
		t.Errorf("Rust SDK missing post_account method")
	}
}

func TestCppSDK_GeneratesSelfDocumentingSDK(t *testing.T) {
	app := crossApp()
	sdk, err := app.CppSDK("ledger")
	if err != nil {
		t.Fatalf("CppSDK error: %v", err)
	}
	hdr := string(sdk.Header)
	t.Logf("Generated C++ SDK Header:\n%s", hdr)

	// Check Doxygen doc comments
	if !strings.Contains(hdr, "* GetAccount retrieves account details by ID.") {
		t.Errorf("C++ SDK missing Doxygen comment")
	}
	if !strings.Contains(hdr, "struct AccountIn") {
		t.Errorf("C++ SDK missing AccountIn struct")
	}
	if !strings.Contains(hdr, "struct AccountOut") {
		t.Errorf("C++ SDK missing AccountOut struct")
	}
	if !strings.Contains(hdr, "std::uint64_t balance") {
		t.Errorf("C++ SDK missing std::uint64_t balance field")
	}
	if !strings.Contains(hdr, "virtual AccountOut post_account") {
		t.Errorf("C++ SDK missing post_account method")
	}
}

func TestRustMCP_GeneratesSelfDocumentingTools(t *testing.T) {
	app := crossApp()
	mcp, err := app.RustMCP("ledger")
	if err != nil {
		t.Fatalf("RustMCP error: %v", err)
	}
	src := string(mcp.Source)

	if !strings.Contains(src, "pub static TOOLS: &[ToolDescriptor]") {
		t.Errorf("Rust MCP missing TOOLS catalogue")
	}
	if !strings.Contains(src, "pub trait McpHandler") {
		t.Errorf("Rust MCP missing McpHandler trait")
	}
	if !strings.Contains(src, "pub async fn dispatch<H: McpHandler>") {
		t.Errorf("Rust MCP missing dispatch function")
	}
}

func TestCppMCP_GeneratesSelfDocumentingTools(t *testing.T) {
	app := crossApp()
	mcp, err := app.CppMCP("ledger")
	if err != nil {
		t.Fatalf("CppMCP error: %v", err)
	}
	hdr := string(mcp.Header)

	if !strings.Contains(hdr, "class McpHandler") {
		t.Errorf("C++ MCP missing McpHandler class")
	}
	if !strings.Contains(hdr, "inline std::vector<ToolDescriptor> list_tools()") {
		t.Errorf("C++ MCP missing list_tools()")
	}
	if !strings.Contains(hdr, "inline nlohmann::json dispatch") {
		t.Errorf("C++ MCP missing dispatch()")
	}
}

func TestDocsMarkdown_GeneratesHanzoDocsPages(t *testing.T) {
	app := crossApp()
	bundle, err := app.DocsMarkdown("Ledger Service")
	if err != nil {
		t.Fatalf("DocsMarkdown error: %v", err)
	}

	if !strings.Contains(bundle.Index, "title: \"Ledger Service\"") {
		t.Errorf("Docs index missing title")
	}
	if len(bundle.Pages) == 0 {
		t.Fatalf("Docs bundle has no pages")
	}

	for _, content := range bundle.Pages {
		if !strings.Contains(content, "<Tabs items={['Go', 'Rust', 'C++']}>") {
			t.Errorf("Doc page missing multi-language tabs")
		}
		if !strings.Contains(content, "<Tab value=\"Go\">") {
			t.Errorf("Doc page missing Go tab")
		}
		if !strings.Contains(content, "<Tab value=\"Rust\">") {
			t.Errorf("Doc page missing Rust tab")
		}
		if !strings.Contains(content, "<Tab value=\"C++\">") {
			t.Errorf("Doc page missing C++ tab")
		}
		if !strings.Contains(content, "## Request Parameters") {
			t.Errorf("Doc page missing request parameters table")
		}
	}
}

func TestRustCLI_GeneratesRustCLIBindings(t *testing.T) {
	app := crossApp()
	cli, err := app.RustCLI("ledger")
	if err != nil {
		t.Fatalf("RustCLI error: %v", err)
	}
	src := string(cli.Source)
	if !strings.Contains(src, "pub struct CommandDescriptor") {
		t.Errorf("Rust CLI missing CommandDescriptor struct")
	}
	if !strings.Contains(src, "pub trait CliRunner") {
		t.Errorf("Rust CLI missing CliRunner trait")
	}
	if !strings.Contains(src, "pub async fn run_cli") {
		t.Errorf("Rust CLI missing run_cli dispatcher")
	}
}

func TestCppCLI_GeneratesCppCLIBindings(t *testing.T) {
	app := crossApp()
	cli, err := app.CppCLI("ledger")
	if err != nil {
		t.Fatalf("CppCLI error: %v", err)
	}
	hdr := string(cli.Header)
	if !strings.Contains(hdr, "struct CommandDescriptor") {
		t.Errorf("C++ CLI missing CommandDescriptor struct")
	}
	if !strings.Contains(hdr, "class CliRunner") {
		t.Errorf("C++ CLI missing CliRunner class")
	}
	if !strings.Contains(hdr, "inline std::string run_cli") {
		t.Errorf("C++ CLI missing run_cli dispatcher")
	}
}

