package pkgmeta

import (
	"errors"
	"strings"
	"testing"
)

func mcpStdioDraft() Package {
	return Package{Name: "codeword", Version: "1.0.0", Publisher: "acme", Runtime: RuntimeStdio,
		MCP: &MCP{Tools: []MCPTool{{Name: "get_codeword"}}, Env: []MCPEnv{{Name: "CODEWORD_SALT", Secret: true, Required: true}},
			Credentials: MCPCredentialsMember, Stdio: &MCPStdio{Args: []string{"--stdio"}}}}
}

const pinSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestPrepareAndAssembleMCPStdio(t *testing.T) {
	prepared, err := PrepareMCP(mcpStdioDraft(), []string{"darwin-arm64", "linux-amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Kind != KindMCPPackageV1 || prepared.Contract != MCPContractV1 || len(prepared.Artifacts) != 2 {
		t.Fatalf("prepared = %+v", prepared)
	}
	pin := Artifact{URL: "https://gw.example.com/v1/artifacts/sha256/" + pinSHA, SHA256: pinSHA}
	if _, err := AssembleMCP(prepared, map[string]Artifact{"darwin-arm64": pin}); err == nil {
		t.Fatal("a missing platform must be refused")
	}
	if _, err := AssembleMCP(prepared, map[string]Artifact{"darwin-arm64": pin, "windows-amd64": pin}); err == nil {
		t.Fatal("an unprepared platform must be refused")
	}
	if _, err := AssembleMCP(prepared, map[string]Artifact{"darwin-arm64": pin, "linux-amd64": {}}); err == nil || !strings.Contains(err.Error(), "uploaded") {
		t.Fatalf("a placeholder must be refused, err = %v", err)
	}
	pkg, err := AssembleMCP(prepared, map[string]Artifact{"darwin-arm64": pin, "linux-amd64": pin})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Artifacts["linux-amd64"] != pin {
		t.Fatalf("artifacts = %+v", pkg.Artifacts)
	}
	// The prepared draft keeps its placeholders (the assembly copies).
	if prepared.Artifacts["linux-amd64"] != (Artifact{}) {
		t.Fatal("AssembleMCP changed the prepared draft")
	}
}

func TestPrepareMCPEndpoint(t *testing.T) {
	draft := Package{Name: "search", Version: "2.0.0", Publisher: "acme", Runtime: RuntimeEndpoint,
		Endpoint: &Endpoint{BaseURL: "https://tools.example.com/mcp"}, Permissions: Permissions{Network: []string{"tools.example.com"}},
		MCP: &MCP{Tools: []MCPTool{{Name: "search"}}, HTTP: &MCPHTTP{Auth: MCPHTTPAuth{Scheme: MCPAuthNone}}}}
	prepared, err := PrepareMCP(draft, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AssembleMCP(prepared, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareMCP(draft, []string{"darwin-arm64"}); !errors.Is(err, ErrMCPEndpointBinaries) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareMCPRefusals(t *testing.T) {
	if _, err := PrepareMCP(mcpStdioDraft(), nil); !errors.Is(err, ErrMCPNeedsBinaries) {
		t.Fatalf("no binary: err = %v", err)
	}
	if _, err := PrepareMCP(mcpStdioDraft(), []string{"darwin-arm64", "darwin-arm64"}); err == nil {
		t.Fatal("a platform named twice must be refused")
	}
	other := mcpStdioDraft()
	other.Kind = KindCorePackageV1
	if _, err := PrepareMCP(other, []string{"darwin-arm64"}); err == nil || !strings.Contains(err.Error(), "is not "+KindMCPPackageV1) {
		t.Fatalf("another kind: err = %v", err)
	}
	invalid := mcpStdioDraft()
	invalid.MCP.Credentials = ""
	if _, err := PrepareMCP(invalid, []string{"darwin-arm64"}); err == nil || !strings.Contains(err.Error(), "mcp.credentials is required") {
		t.Fatalf("invalid: err = %v", err)
	}
}
