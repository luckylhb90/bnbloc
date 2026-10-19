package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// mcpFormGateway is the content gateway with sam, a super admin.
func mcpFormGateway(t *testing.T) *contentGateway {
	t.Helper()
	g := newContentGateway(t)
	prev := g.svc.TenantResolver
	g.svc.TenantResolver = func(id auth.Identity, hint string) (string, string, error) {
		if id.Subject == "sam" {
			return "home", string(rbac.RoleSuperAdmin), nil
		}
		return prev(id, hint)
	}
	g.tokens["sam"], _ = login(t, g.srv, "sam")
	return g
}

// stageBinary uploads bytes through the console's artifact staging route
// and returns the key it answered.
func stageBinary(t *testing.T, g *contentGateway, data string) string {
	t.Helper()
	status, raw := g.do("sam", http.MethodPost, "/v1/platform/artifacts", "application/octet-stream", []byte(data))
	if status != http.StatusOK {
		t.Fatalf("stage: %d %s", status, raw)
	}
	var put struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &put); err != nil || put.Key == "" {
		t.Fatalf("stage reply %s: %v", raw, err)
	}
	return put.Key
}

func stdioMCPDraft(key string) map[string]any {
	return map[string]any{
		"name": "codeword", "version": "1.0.0", "publisher": "acme", "runtime": "stdio",
		"mcp": map[string]any{
			"tools": []any{map[string]any{"name": "get_codeword", "description": "Answers the codeword."}},
			"stdio": map[string]any{"args": []any{"--stdio"}},
		},
		"settings":  []any{map[string]any{"key": "salt", "label": "Salt", "type": "secret", "scope": "member", "required": true, "deliver": map[string]any{"env": "CODEWORD_SALT"}}},
		"artifacts": map[string]any{"darwin-arm64": map[string]any{"key": key}},
	}
}

// The console's form assembles a stdio tool: the staged binary is pinned to
// its gateway URL and sha256, kind and contract are filled, and the answer
// is the package to sign (installable as is).
func TestBuildMCPPackageStdio(t *testing.T) {
	g := mcpFormGateway(t)
	key := stageBinary(t, g, "codeword binary")
	if status, _ := g.json("maya", http.MethodPost, "/v1/platform/mcp-packages", stdioMCPDraft(key)); status != http.StatusForbidden {
		t.Fatalf("member: status = %d, want 403", status)
	}
	req, _ := http.NewRequest(http.MethodPost, g.srv.URL+"/v1/platform/mcp-packages", strings.NewReader(mustJSON(t, stdioMCPDraft(key))))
	req.Header.Set("Authorization", "Bearer "+g.tokens["sam"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `codeword-1.0.0.package.json`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	var pkg pkgmeta.Package
	if err := json.NewDecoder(resp.Body).Decode(&pkg); err != nil {
		t.Fatal(err)
	}
	a := pkg.Artifacts["darwin-arm64"]
	if pkg.Kind != pkgmeta.KindMCPPackageV1 || pkg.Contract != pkgmeta.MCPContractV1 || "sha256/"+a.SHA256 != key ||
		a.URL != g.srv.URL+"/v1/artifacts/"+key || pkg.MCP.Stdio.Args[0] != "--stdio" || pkg.Settings[0].Deliver.Env != "CODEWORD_SALT" {
		t.Fatalf("pkg = %+v", pkg)
	}
	if err := pkgmeta.ValidatePackage(pkg); err != nil {
		t.Fatalf("assembled package invalid: %v", err)
	}
}

// An http tool takes no binaries; its URL, auth and network permission go
// into the package.
func TestBuildMCPPackageEndpoint(t *testing.T) {
	g := mcpFormGateway(t)
	draft := map[string]any{
		"name": "search", "version": "2.0.0", "publisher": "acme", "runtime": "endpoint",
		"endpoint": map[string]any{"baseUrl": "https://tools.example.com/mcp"}, "permissions": map[string]any{"network": []any{"tools.example.com"}},
		"mcp": map[string]any{
			"tools": []any{map[string]any{"name": "search"}},
			"http":  map[string]any{"auth": map[string]any{"scheme": "bearer", "setting": "token"}},
		},
		"settings": []any{map[string]any{"key": "token", "label": "API token", "type": "secret", "scope": "org", "required": true}},
	}
	status, raw := g.json("sam", http.MethodPost, "/v1/platform/mcp-packages", draft)
	if status != http.StatusOK {
		t.Fatalf("status = %d %s", status, raw)
	}
	pkg, err := pkgmeta.ParsePackage(raw)
	if err != nil || pkg.Runtime != pkgmeta.RuntimeEndpoint || pkg.MCP.HTTP.Auth.Scheme != "bearer" || len(pkg.Artifacts) != 0 {
		t.Fatalf("pkg = %+v err = %v", pkg, err)
	}
}

// Invalid drafts are 400s with the reason; nothing is assembled.
func TestBuildMCPPackageRefusals(t *testing.T) {
	g := mcpFormGateway(t)
	key := stageBinary(t, g, "codeword binary")
	missing := "sha256/" + strings.Repeat("b", 64)
	badSetting := stdioMCPDraft(key)
	badSetting["settings"].([]any)[0].(map[string]any)["deliver"] = map[string]any{"env": "LD_PRELOAD"}
	noBinary := stdioMCPDraft(key)
	delete(noBinary, "artifacts")
	badKey := stdioMCPDraft("not-a-key")
	unknownField := stdioMCPDraft(key)
	unknownField["contractz"] = "x"
	for name, c := range map[string]struct {
		draft map[string]any
		want  string
	}{
		"hijacking setting": {badSetting, "changes how a process runs"},
		"stdio no binary":   {noBinary, "at least one platform binary"},
		"bad key":           {badKey, "sha256/<hex>"},
		"unknown field":     {unknownField, "invalid json body"},
		"unstaged artifact": {stdioMCPDraft(missing), "not found"},
	} {
		status, raw := g.json("sam", http.MethodPost, "/v1/platform/mcp-packages", c.draft)
		if status != http.StatusBadRequest || !strings.Contains(string(raw), c.want) {
			t.Errorf("%s: %d %s, want 400 %q", name, status, raw, c.want)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
