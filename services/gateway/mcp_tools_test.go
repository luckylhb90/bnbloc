package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// mcpGateway is the content gateway with two installed MCP tools: codeword
// (stdio, member credentials) and search (http, org credentials), and ada,
// an org admin of "home".
func mcpGateway(t *testing.T, withKey bool) (*contentGateway, string) {
	t.Helper()
	g := newContentGateway(t)
	resolve := g.svc.TenantResolver
	g.svc.TenantResolver = func(id auth.Identity, hint string) (string, string) {
		if id.Subject == "ada" {
			return "home", string(rbac.RoleAdmin)
		}
		return resolve(id, hint)
	}
	g.tokens["ada"], _ = login(t, g.srv, "ada")
	if withKey {
		box, err := sealbox.New(bytes.Repeat([]byte{9}, 32))
		if err != nil {
			t.Fatal(err)
		}
		g.svc.SecretBox = box
	}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(endpoint.Close)
	g.svc.AllowExtUnsigned = true
	g.svc.ExtPipe = nil
	sha := strings.Repeat("a", 64)
	stdio := pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: "codeword", Version: "1.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeStdio,
		Artifacts: map[string]pkgmeta.Artifact{"darwin-arm64": {URL: "https://gw.example/v1/artifacts/sha256/" + sha, SHA256: sha}},
		MCP: &pkgmeta.MCP{
			Tools:       []pkgmeta.MCPTool{{Name: "get_codeword"}},
			Env:         []pkgmeta.MCPEnv{{Name: "CODEWORD_SALT", Secret: true, Required: true}, {Name: "CODEWORD_LANG"}},
			Credentials: pkgmeta.MCPCredentialsMember, Stdio: &pkgmeta.MCPStdio{Args: []string{"serve"}},
		},
	}
	u, _ := url.Parse(endpoint.URL)
	httpPkg := pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: "search", Version: "2.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeEndpoint,
		Endpoint: &pkgmeta.Endpoint{BaseURL: endpoint.URL + "/mcp"}, Permissions: pkgmeta.Permissions{Network: []string{u.Hostname()}},
		MCP: &pkgmeta.MCP{
			Tools:       []pkgmeta.MCPTool{{Name: "search"}},
			Env:         []pkgmeta.MCPEnv{{Name: "TOOLS_TOKEN", Secret: true, Required: true}},
			Credentials: pkgmeta.MCPCredentialsOrg,
			HTTP:        &pkgmeta.MCPHTTP{Auth: pkgmeta.MCPHTTPAuth{Scheme: pkgmeta.MCPAuthBearer, Env: "TOOLS_TOKEN"}},
		},
	}
	for _, pkg := range []pkgmeta.Package{stdio, httpPkg} {
		raw, _ := json.Marshal(pkg)
		if _, err := g.svc.corePipeline().Install(context.Background(), extpipe.InstallRequest{Body: raw, Actor: "root"}); err != nil {
			t.Fatalf("install %s: %v", pkg.Name, err)
		}
	}
	return g, u.Host
}

func mcpExpert(requires map[string]any) map[string]any {
	form := expertForm("Use the codeword tool.")
	form["form"].(map[string]any)["requires"] = requires
	return form
}

// A save's requires.mcp must name an MCP component the author may use, only
// its variables, with the permission its transport needs.
func TestContentSaveChecksMCPReferences(t *testing.T) {
	g, host := mcpGateway(t, false)
	cases := []struct {
		requires map[string]any
		code     int
		want     string
	}{
		{map[string]any{"mcp": []map[string]any{{"name": "ghost"}}, "permissions": []string{"mcp.stdio"}}, 400, "not an MCP tool component"},
		{map[string]any{"mcp": []map[string]any{{"name": "codeword"}}}, 400, `needs the "mcp.stdio" permission`},
		{map[string]any{"mcp": []map[string]any{{"name": "codeword", "env": []string{"NOPE"}}}, "permissions": []string{"mcp.stdio"}}, 400, "not one of its variables"},
		{map[string]any{"mcp": []map[string]any{{"name": "search"}}, "permissions": []string{"mcp.http:" + host}}, 201, ""},
	}
	for i, c := range cases {
		code, raw := g.json("maya", http.MethodPost, "/v1/content-components", mcpExpert(c.requires))
		if code != c.code || !strings.Contains(string(raw), c.want) {
			t.Fatalf("case %d = %d %s", i, code, raw)
		}
	}
	var d wire.ContentComponentDetail
	_, raw := g.do("maya", http.MethodGet, "/v1/content-components?kind=expert", "", nil)
	_ = json.Unmarshal(raw, &d)
	if !strings.Contains(string(raw), `"mcp":["search"]`) {
		t.Fatalf("the card lists its MCP tools: %s", raw)
	}
}

// The member catalog lists MCP tools with their tools, env and credentials
// source; an org tool says whether the member's organization configured it.
func TestMemberCatalogListsMCPTools(t *testing.T) {
	g, _ := mcpGateway(t, true)
	code, raw := g.do("maya", http.MethodGet, "/v1/components?kind=mcp", "", nil)
	if code != http.StatusOK {
		t.Fatalf("catalog = %d %s", code, raw)
	}
	var out struct {
		Components []struct {
			Name            string                      `json:"name"`
			Runtime         string                      `json:"runtime"`
			RequiresDesktop bool                        `json:"requiresDesktop"`
			Artifacts       map[string]pkgmeta.Artifact `json:"artifacts"`
			MCP             *struct {
				Transport     string            `json:"transport"`
				Credentials   string            `json:"credentials"`
				Tools         []pkgmeta.MCPTool `json:"tools"`
				OrgConfigured *bool             `json:"orgConfigured"`
			} `json:"mcp"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Components) != 2 {
		t.Fatalf("catalog = %s", raw)
	}
	for _, c := range out.Components {
		switch c.Name {
		case "codeword":
			if c.Runtime != "stdio" || !c.RequiresDesktop || c.Artifacts["darwin-arm64"].SHA256 == "" || c.MCP.Transport != "stdio" || c.MCP.OrgConfigured != nil {
				t.Fatalf("codeword = %+v", c)
			}
		case "search":
			if c.MCP.Transport != "http" || c.MCP.Credentials != "org" || c.MCP.OrgConfigured == nil || *c.MCP.OrgConfigured {
				t.Fatalf("search = %+v", c)
			}
		}
	}
}

// Org connectors: org admins only, sealed at rest, secret values never
// returned, off without the secret store key, gone with the component.
func TestOrgMCPConnectors(t *testing.T) {
	off, _ := mcpGateway(t, false)
	if code, raw := off.do("ada", http.MethodGet, "/v1/tenant/mcp-connectors", "", nil); code != http.StatusOK || !strings.Contains(string(raw), `"available":false`) {
		t.Fatalf("list without key = %d %s", code, raw)
	}
	if code, _ := off.json("ada", http.MethodPut, "/v1/tenant/mcp-connectors/search", map[string]any{"values": map[string]string{"TOOLS_TOKEN": "x"}}); code != http.StatusConflict {
		t.Fatalf("put without key = %d", code)
	}

	g, _ := mcpGateway(t, true)
	if code, _ := g.do("maya", http.MethodGet, "/v1/tenant/mcp-connectors", "", nil); code != http.StatusForbidden {
		t.Fatalf("member list = %d", code)
	}
	if code, _ := g.json("maya", http.MethodPut, "/v1/tenant/mcp-connectors/search", map[string]any{"values": map[string]string{"TOOLS_TOKEN": "x"}}); code != http.StatusForbidden {
		t.Fatalf("member put = %d", code)
	}
	if code, _ := g.json("ada", http.MethodPut, "/v1/tenant/mcp-connectors/codeword", map[string]any{"values": map[string]string{}}); code != http.StatusBadRequest {
		t.Fatalf("member-credential tool takes no connector = %d", code)
	}
	if code, raw := g.json("ada", http.MethodPut, "/v1/tenant/mcp-connectors/search", map[string]any{"values": map[string]string{}}); code != http.StatusBadRequest || !strings.Contains(string(raw), "TOOLS_TOKEN is required") {
		t.Fatalf("missing required = %d %s", code, raw)
	}
	if code, raw := g.json("ada", http.MethodPut, "/v1/tenant/mcp-connectors/search", map[string]any{"values": map[string]string{"TOOLS_TOKEN": "s3cr3t-token"}}); code != http.StatusNoContent {
		t.Fatalf("put = %d %s", code, raw)
	}
	code, raw := g.do("ada", http.MethodGet, "/v1/tenant/mcp-connectors", "", nil)
	var list wire.MCPConnectorsReply
	_ = json.Unmarshal(raw, &list)
	if code != http.StatusOK || !list.Available || len(list.Connectors) != 1 || !list.Connectors[0].Configured || !list.Connectors[0].Fields[0].Set || list.Connectors[0].UpdatedBy != "ada" {
		t.Fatalf("list = %d %s", code, raw)
	}
	if strings.Contains(string(raw), "s3cr3t-token") {
		t.Fatal("a secret value is never returned")
	}
	rows, _ := g.svc.PlatformSettings.ListPlatformSettings(context.Background())
	for _, row := range rows {
		if strings.Contains(row.Value, "s3cr3t-token") {
			t.Fatalf("stored in clear under %s", row.Key)
		}
	}
	if _, raw := g.do("maya", http.MethodGet, "/v1/components?kind=mcp", "", nil); !strings.Contains(string(raw), `"orgConfigured":true`) {
		t.Fatalf("catalog = %s", raw)
	}
	// Uninstalling the component deletes every organization's connector.
	g.tokens["root"] = superAdminToken(t, g)
	if code, raw := g.do("root", http.MethodDelete, "/v1/platform/components/search?kind=mcp", "", nil); code != http.StatusNoContent {
		t.Fatalf("uninstall = %d %s", code, raw)
	}
	if _, ok, _ := g.svc.mcpConnectors().Get(context.Background(), "home", "search"); ok {
		t.Fatal("connector survived its component")
	}
}

// superAdminToken signs in a super admin of the content gateway.
func superAdminToken(t *testing.T, g *contentGateway) string {
	t.Helper()
	resolve := g.svc.TenantResolver
	g.svc.TenantResolver = func(id auth.Identity, hint string) (string, string) {
		if id.Subject == "root" {
			return "home", string(rbac.RoleSuperAdmin)
		}
		return resolve(id, hint)
	}
	g.svc.SuperAdmins = append(g.svc.SuperAdmins, "root")
	tok, _ := login(t, g.srv, "root")
	return tok
}

// The effective resolution of a job lists the MCP tools its content
// requires (with an org connector's values for the bridge holding the
// lease) and reports the unmet ones as missing.
func TestContentEffectiveResolvesMCP(t *testing.T) {
	g, host := mcpGateway(t, true)
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components", mcpExpert(map[string]any{
		"mcp":         []map[string]any{{"name": "codeword", "env": []string{"CODEWORD_LANG"}}, {"name": "search"}},
		"permissions": []string{"mcp.stdio", "mcp.http:" + host},
	}))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	ex := decodeDetail(t, raw)
	ctx := tenant.NewContext(context.Background(), "home")
	_ = g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()})
	job, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 3, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", ExpertID: ex.Component.ID, ExpertVersion: "1.0.0", ExpertName: "Reviewer", CreatedAtMs: 1})
	if err != nil {
		t.Fatal(err)
	}
	effective := func() wire.ContentEffective {
		t.Helper()
		code, raw := g.do("jack", http.MethodGet, "/v1/content-components/effective?job="+job.JobID+"&room=room-1", "", nil)
		if code != http.StatusOK {
			t.Fatalf("effective = %d %s", code, raw)
		}
		var eff wire.ContentEffective
		_ = json.Unmarshal(raw, &eff)
		return eff
	}
	eff := effective()
	if len(eff.MCP) != 1 || eff.MCP[0].Name != "codeword" || eff.MCP[0].Transport != "stdio" || eff.MCP[0].Args[0] != "serve" ||
		eff.MCP[0].Artifacts["darwin-arm64"] != strings.Repeat("a", 64) || !eff.MCP[0].Env[1].Required || eff.MCP[0].RequiredBy[0] != ex.Component.ID {
		t.Fatalf("mcp = %+v", eff.MCP)
	}
	if len(eff.Missing) != 1 || !strings.Contains(eff.Missing[0], "search") {
		t.Fatalf("an unconfigured org connector is missing: %v", eff.Missing)
	}
	if code, _ := g.json("ada", http.MethodPut, "/v1/tenant/mcp-connectors/search", map[string]any{"values": map[string]string{"TOOLS_TOKEN": "tok"}}); code != http.StatusNoContent {
		t.Fatalf("put = %d", code)
	}
	eff = effective()
	if len(eff.MCP) != 2 || len(eff.Missing) != 0 {
		t.Fatalf("mcp = %+v missing = %v", eff.MCP, eff.Missing)
	}
	s := eff.MCP[1]
	if s.Transport != "http" || !strings.HasSuffix(s.URL, "/mcp") || s.Auth.Scheme != "bearer" || s.Values["TOOLS_TOKEN"] != "tok" {
		t.Fatalf("search = %+v", s)
	}
	// A tool the requester may no longer use (rollout off) is missing.
	g.tokens["root"] = superAdminToken(t, g)
	if code, raw := g.json("root", http.MethodPut, "/v1/platform/components/codeword/rollout?kind=mcp", map[string]string{"rollout": "off"}); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("rollout = %d %s", code, raw)
	}
	eff = effective()
	if len(eff.MCP) != 1 || len(eff.Missing) != 1 || eff.Missing[0] != "MCP tool codeword" {
		t.Fatalf("mcp = %+v missing = %v", eff.MCP, eff.Missing)
	}
}
