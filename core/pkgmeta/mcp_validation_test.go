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
			"tools": []map[string]any{{"name": "get_codeword", "description": "Returns today's codeword"}},
			"stdio": map[string]any{"args": []string{"--stdio"}},
		},
		"settings": []map[string]any{
			{"key": "salt", "label": "Salt", "type": "secret", "scope": "member", "required": true, "deliver": map[string]any{"env": "CODEWORD_SALT"}},
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
			"tools": []map[string]any{{"name": "search"}},
			"http":  map[string]any{"auth": map[string]any{"scheme": "bearer", "setting": "token"}},
		}
		b["settings"] = []map[string]any{{"key": "token", "label": "API token", "type": "secret", "scope": "org", "required": true}}
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
	if pkg.MCP == nil || pkg.MCP.Credentials != "" || pkg.MCP.Stdio.Args[0] != "--stdio" || pkg.Settings[0].Deliver.Env != "CODEWORD_SALT" {
		t.Fatalf("stdio payload = %+v %+v", pkg.MCP, pkg.Settings)
	}
	if _, err := ParsePackage(mcpEndpointBody(t, nil)); err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	// No settings, no auth: a public endpoint.
	if _, err := ParsePackage(mcpEndpointBody(t, func(b map[string]any) {
		b["mcp"] = map[string]any{"tools": []map[string]any{{"name": "t"}}}
		delete(b, "settings")
	})); err != nil {
		t.Fatalf("public endpoint: %v", err)
	}
	// A named header, and a further setting in a header of its own.
	if _, err := ParsePackage(mcpEndpointBody(t, func(b map[string]any) {
		mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "header", "header": "X-Api-Key", "setting": "token"}}
		b["settings"] = append(b["settings"].([]map[string]any),
			map[string]any{"key": "workspace", "label": "Workspace", "type": "text", "scope": "member", "deliver": map[string]any{"header": "X-Workspace"}})
	})); err != nil {
		t.Fatalf("header auth: %v", err)
	}
	// A setting delivered as the bearer, no auth block.
	if _, err := ParsePackage(mcpEndpointBody(t, func(b map[string]any) {
		delete(mcpBlock(b), "http")
		b["settings"] = []map[string]any{{"key": "token", "label": "Token", "type": "secret", "scope": "platform", "deliver": map[string]any{"bearer": true}}}
	})); err != nil {
		t.Fatalf("deliver.bearer: %v", err)
	}
	// A member OAuth sign-in: no setting carries its credential.
	pkg, err = ParsePackage(mcpEndpointBody(t, func(b map[string]any) {
		delete(b, "settings")
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
	// The room as the credential, beside settings of its own.
	if _, err := ParsePackage(mcpStdioBody(t, func(b map[string]any) {
		mcpBlock(b)["credentials"] = MCPCredentialsRoom
		b["settings"] = []map[string]any{{"key": "chrome", "label": "Chrome binary", "type": "path", "scope": "member", "deliver": map[string]any{"env": "BNBLOC_CODEWORD_CHROME"}}}
	})); err != nil {
		t.Fatalf("room credentials: %v", err)
	}
}

func TestMCPStdioPlacement(t *testing.T) {
	pkg, err := ParsePackage(mcpStdioBody(t, func(b map[string]any) {
		mcpBlock(b)["stdio"] = map[string]any{"args": []string{"--stdio"}, "placement": "host", "privatePaths": []string{"acme/mail", "acme/mail-cache"}}
	}))
	if err != nil {
		t.Fatalf("host placement: %v", err)
	}
	if !pkg.MCP.Stdio.RunsOnHost() || len(pkg.MCP.Stdio.PrivatePaths) != 2 {
		t.Fatalf("stdio = %+v", pkg.MCP.Stdio)
	}
	// The default and the explicit "agent" are the same, and run in the agent's tree.
	for _, placement := range []string{"", "agent"} {
		pkg, err := ParsePackage(mcpStdioBody(t, func(b map[string]any) {
			mcpBlock(b)["stdio"] = map[string]any{"placement": placement}
		}))
		if err != nil || pkg.MCP.Stdio.RunsOnHost() {
			t.Fatalf("placement %q: runsOnHost=%v err=%v", placement, err == nil && pkg.MCP.Stdio.RunsOnHost(), err)
		}
	}
	if (*MCPStdio)(nil).RunsOnHost() {
		t.Fatal("no stdio block runs on the host")
	}
}

func TestMCPStdioPlacementRefusals(t *testing.T) {
	stdio := func(placement string, paths ...string) func(map[string]any) {
		return func(b map[string]any) {
			st := map[string]any{"placement": placement}
			if paths != nil {
				st["privatePaths"] = paths
			}
			mcpBlock(b)["stdio"] = st
		}
	}
	long := strings.Repeat("a", maxMCPPrivatePath+1)
	many := make([]string, maxMCPPrivatePaths+1)
	for i := range many {
		many[i] = "acme/p" + string(rune('a'+i))
	}
	for name, c := range map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"unknown placement":        {stdio("vm"), "placement must be"},
		"paths on an agent tool":   {stdio("agent", "acme/mail"), "belong to placement"},
		"paths with no placement":  {stdio("", "acme/mail"), "belong to placement"},
		"absolute path":            {stdio("host", "/etc"), "plain relative path"},
		"parent escape":            {stdio("host", "../.ssh"), "plain relative path"},
		"embedded parent":          {stdio("host", "acme/../.ssh"), "plain relative path"},
		"dot":                      {stdio("host", "."), "plain relative path"},
		"unclean path":             {stdio("host", "acme//mail"), "plain relative path"},
		"backslash":                {stdio("host", `acme\mail`), "plain relative path"},
		"empty path":               {stdio("host", ""), "at most"},
		"too long":                 {stdio("host", long), "at most"},
		"too many":                 {stdio("host", many...), "at most"},
		"placement on an endpoint": {func(b map[string]any) { mcpBlock(b)["stdio"] = map[string]any{"placement": "host"} }, "must not declare mcp.stdio"},
	} {
		t.Run(name, func(t *testing.T) {
			body := mcpStdioBody
			if name == "placement on an endpoint" {
				body = mcpEndpointBody
			}
			_, err := ParsePackage(body(t, c.mutate))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
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
		{"unknown credentials", mcpStdioBody, func(b map[string]any) { mcpBlock(b)["credentials"] = "member" }, `mcp.credentials must be "room"`},
		{"mcp.env is gone", mcpStdioBody, func(b map[string]any) {
			mcpBlock(b)["env"] = []map[string]any{{"name": "TOKEN"}}
		}, "unknown field"},
		{"room credentials on an endpoint", mcpEndpointBody, func(b map[string]any) {
			b["mcp"] = map[string]any{"tools": []map[string]any{{"name": "t"}}, "credentials": "room"}
		}, `for runtime "stdio" only`},
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
		{"endpoint auth names no setting", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "bearer", "setting": "other"}}
		}, "auth.setting must name"},
		{"endpoint setting that never reaches it", mcpEndpointBody, func(b map[string]any) { delete(mcpBlock(b), "http") }, "never reaches an http MCP tool"},
		{"endpoint auth setting delivered twice", mcpEndpointBody, func(b map[string]any) {
			b["settings"].([]map[string]any)[0]["deliver"] = map[string]any{"header": "X-Token"}
		}, "it declares no deliver"},
		{"endpoint second bearer", mcpEndpointBody, func(b map[string]any) {
			b["settings"] = append(b["settings"].([]map[string]any),
				map[string]any{"key": "other", "label": "Other", "type": "secret", "scope": "org", "deliver": map[string]any{"bearer": true}})
		}, `scheme "bearer" is the bearer`},
		{"endpoint env delivery", mcpEndpointBody, func(b map[string]any) {
			b["settings"] = append(b["settings"].([]map[string]any),
				map[string]any{"key": "region", "label": "Region", "type": "text", "scope": "org", "deliver": map[string]any{"env": "REGION", "header": "X-Region"}})
		}, "only a plugin or a stdio MCP tool"},
		{"endpoint bad header", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "header", "header": "X Bad", "setting": "token"}}
		}, "header name"},
		{"endpoint transport header", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "header", "header": "Mcp-Session-Id", "setting": "token"}}
		}, "set by the MCP client"},
		{"oauth with a setting", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "oauth", "setting": "token"}}
		}, "takes no setting or header"},
		{"oauth with a bearer setting", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "oauth"}}
			b["settings"].([]map[string]any)[0]["deliver"] = map[string]any{"bearer": true}
		}, "the bearer is the member's sign-in"},
		{"oauth bad scope", mcpEndpointBody, func(b map[string]any) {
			b["mcp"] = map[string]any{"tools": []map[string]any{{"name": "t"}}, "http": map[string]any{"auth": map[string]any{"scheme": "oauth", "scopes": []string{"a b"}}}}
		}, "is not an OAuth scope"},
		{"oauth bad client id", mcpEndpointBody, func(b map[string]any) {
			b["mcp"] = map[string]any{"tools": []map[string]any{{"name": "t"}}, "http": map[string]any{"auth": map[string]any{"scheme": "oauth", "clientId": "has space"}}}
		}, "clientId must be"},
		{"scopes without oauth", mcpEndpointBody, func(b map[string]any) {
			mcpBlock(b)["http"] = map[string]any{"auth": map[string]any{"scheme": "bearer", "setting": "token", "scopes": []string{"x"}}}
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
