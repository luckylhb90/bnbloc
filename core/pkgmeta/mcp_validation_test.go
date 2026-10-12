package pkgmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func mcpStdioBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"kind":     KindMCPPackageV1,
		"name":     "codeword",
		"version":  "1.0.0",
		"runtime":  "stdio",
		"contract": MCPContractV1,
		"artifacts": map[string]any{
			"darwin-arm64": sandboxArtifact('a'),
			"linux-amd64":  sandboxArtifact('b'),
		},
		"mcp": map[string]any{
			"tools":       []map[string]any{{"name": "get_codeword", "description": "Returns today's codeword"}},
			"env":         []map[string]any{{"name": "CODEWORD_SALT", "description": "salt", "secret": true, "required": true}},
			"credentials": "member",
			"stdio":       map[string]any{"args": []string{"--stdio"}},
		},
		"publisher":   "Acme Corp",
		"description": "codeword tools",
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mcpEndpointBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	return mcpStdioBody(t, func(b map[string]any) {
		b["runtime"] = "endpoint"
		delete(b, "artifacts")
		b["endpoint"] = map[string]any{"baseUrl": "https://tools.example.com/mcp"}
		b["permissions"] = map[string]any{"network": []string{"tools.example.com"}}
		b["mcp"] = map[string]any{
			"tools":       []map[string]any{{"name": "search"}},
			"env":         []map[string]any{{"name": "TOOLS_TOKEN", "secret": true, "required": true}},
			"credentials": "org",
			"http":        map[string]any{"auth": map[string]any{"scheme": "bearer", "env": "TOOLS_TOKEN"}},
		}
		if mutate != nil {
			mutate(b)
		}
	})
}

func mcpBlock(b map[string]any) map[string]any { return b["mcp"].(map[string]any) }

func TestMCPPackageValid(t *testing.T) {
	pkg, err := ParsePackage(mcpStdioBody(t, nil))
	if err != nil {
		t.Fatalf("stdio: %v", err)
	}
	if pkg.MCP == nil || pkg.MCP.Credentials != MCPCredentialsMember || pkg.MCP.Stdio.Args[0] != "--stdio" {
		t.Fatalf("stdio payload = %+v", pkg.MCP)
	}
	if _, err := ParsePackage(mcpEndpointBody(t, nil)); err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	// No env, no credentials, no auth: a public endpoint.
	if _, err := ParsePackage(mcpEndpointBody(t, func(b map[string]any) {
		b["mcp"] = map[string]any{"tools": []map[string]any{{"name": "t"}}}
	})); err != nil {
		t.Fatalf("public endpoint: %v", err)
	}
	// A named header.
	if _, err := ParsePackage(mcpEndpointBody(t, func(b map[string]any) {
		mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "header", "header": "X-Api-Key", "env": "TOOLS_TOKEN"}}
	})); err != nil {
		t.Fatalf("header auth: %v", err)
	}
	// A member OAuth sign-in: no env, no credentials.
	pkg, err = ParsePackage(mcpEndpointBody(t, func(b map[string]any) {
		b["mcp"] = map[string]any{
			"tools": []map[string]any{{"name": "search"}},
			"http":  map[string]any{"auth": map[string]any{"scheme": "oauth", "clientId": "bnbloc-desktop", "scopes": []string{"files:read", "files:write"}}},
		}
	}))
	if err != nil {
		t.Fatalf("oauth auth: %v", err)
	}
	if a := pkg.MCP.HTTP.Auth; a.Scheme != MCPAuthOAuth || a.ClientID != "bnbloc-desktop" || len(a.Scopes) != 2 {
		t.Fatalf("oauth payload = %+v", a)
	}
}

func TestMCPPackageRefusals(t *testing.T) {
	cases := []struct {
		name   string
		body   func(*testing.T, func(map[string]any)) []byte
		mutate func(map[string]any)
		want   string
	}{
		{"contract", mcpStdioBody, func(b map[string]any) { b["contract"] = "x" }, "unknown mcp contract"},
		{"no mcp block", mcpStdioBody, func(b map[string]any) { delete(b, "mcp") }, "requires mcp"},
		{"no tools", mcpStdioBody, func(b map[string]any) { mcpBlock(b)["tools"] = []any{} }, "at least one tool"},
		{"bad tool name", mcpStdioBody, func(b map[string]any) { mcpBlock(b)["tools"] = []map[string]any{{"name": "a b"}} }, "mcp.tools[0].name"},
		{"duplicate tool", mcpStdioBody, func(b map[string]any) {
			mcpBlock(b)["tools"] = []map[string]any{{"name": "a"}, {"name": "a"}}
		}, "duplicate tool"},
		{"lower-case env", mcpStdioBody, func(b map[string]any) {
			mcpBlock(b)["env"] = []map[string]any{{"name": "token"}}
		}, "upper-case identifier"},
		{"reserved env", mcpStdioBody, func(b map[string]any) {
			mcpBlock(b)["env"] = []map[string]any{{"name": "BNBLOC_TOKEN"}}
		}, "reserved"},
		{"env without credentials", mcpStdioBody, func(b map[string]any) { delete(mcpBlock(b), "credentials") }, "mcp.credentials is required"},
		{"credentials without env", mcpStdioBody, func(b map[string]any) { delete(mcpBlock(b), "env") }, "needs mcp.env"},
		{"unknown credentials", mcpStdioBody, func(b map[string]any) { mcpBlock(b)["credentials"] = "everyone" }, "mcp.credentials must be"},
		{"stdio without artifacts", mcpStdioBody, func(b map[string]any) { delete(b, "artifacts") }, "requires artifacts"},
		{"stdio extra file", mcpStdioBody, func(b map[string]any) {
			b["artifacts"].(map[string]any)["darwin-arm64/helper"] = sandboxArtifact('c')
		}, "artifacts.darwin-arm64/helper"},
		{"stdio sidecar", mcpStdioBody, func(b map[string]any) {
			b["sidecar"] = map[string]any{"localUrl": "http://127.0.0.1:1"}
		}, "must not declare sidecar"},
		{"stdio network", mcpStdioBody, func(b map[string]any) {
			b["permissions"] = map[string]any{"network": []string{"x.example"}}
		}, "permissions.network"},
		{"stdio http block", mcpStdioBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "none"}}
		}, "must not declare mcp.http"},
		{"endpoint off allowlist", mcpEndpointBody, func(b map[string]any) {
			b["permissions"] = map[string]any{"network": []string{"other.example.com"}}
		}, "not in permissions.network"},
		{"endpoint artifacts", mcpEndpointBody, func(b map[string]any) {
			b["artifacts"] = map[string]any{"linux-amd64": sandboxArtifact('a')}
		}, "must not declare artifacts"},
		{"endpoint auth env mismatch", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "bearer", "env": "OTHER"}}
		}, "auth.env must name"},
		{"endpoint env without auth", mcpEndpointBody, func(b map[string]any) { delete(mcpBlock(b), "http") }, "without auth takes no env"},
		{"endpoint bad header", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "header", "header": "X Bad", "env": "TOOLS_TOKEN"}}
		}, "header name"},
		{"oauth with org credentials", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "oauth"}}
		}, "never through an organization connector"},
		{"oauth with env", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "oauth", "env": "TOOLS_TOKEN"}}
		}, "takes no env or header"},
		{"oauth bad scope", mcpEndpointBody, func(b map[string]any) {
			b["mcp"] = map[string]any{"tools": []map[string]any{{"name": "t"}}, "http": map[string]any{"auth": map[string]any{"scheme": "oauth", "scopes": []string{"a b"}}}}
		}, "is not an OAuth scope"},
		{"oauth bad client id", mcpEndpointBody, func(b map[string]any) {
			b["mcp"] = map[string]any{"tools": []map[string]any{{"name": "t"}}, "http": map[string]any{"auth": map[string]any{"scheme": "oauth", "clientId": "has space"}}}
		}, "clientId must be"},
		{"scopes without oauth", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "bearer", "env": "TOOLS_TOKEN", "scopes": []string{"x"}}}
		}, `belong to scheme "oauth"`},
		{"unknown runtime", mcpStdioBody, func(b map[string]any) { b["runtime"] = "sidecar" }, `must be "stdio" or "endpoint"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParsePackage(c.body(t, c.mutate))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestMCPBlockOnlyOnMCPPackages(t *testing.T) {
	raw := validSandboxSidecarBody(t, func(b map[string]any) {
		b["mcp"] = map[string]any{"tools": []map[string]any{{"name": "t"}}}
	})
	if _, err := ParsePackage(raw); err == nil || !strings.Contains(err.Error(), "mcp is only valid") {
		t.Fatalf("err = %v", err)
	}
}

func TestMCPStdioArtifactsMustBeUploadedBeforePublish(t *testing.T) {
	pkg, err := ParsePackage(mcpStdioBody(t, func(b map[string]any) {
		b["artifacts"] = map[string]any{"darwin-arm64": map[string]any{"url": "", "sha256": ""}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCompleteArtifacts(pkg); err == nil {
		t.Fatal("a placeholder stdio binary must not publish")
	}
}
