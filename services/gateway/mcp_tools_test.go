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
// (stdio, member settings) and search (http, an organization setting), and
// ada, an org admin of "home".
func mcpGateway(t *testing.T, withKey bool) (*contentGateway, string) {
	t.Helper()
	g := newContentGateway(t)
	resolve := g.svc.TenantResolver
	g.svc.TenantResolver = func(id auth.Identity, hint string) (string, string, error) {
		if id.Subject == "ada" {
			return "home", string(rbac.RoleAdmin), nil
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
	g.svc.ExtPipe = NewCorePipeline(g.svc, g.svc.ExtAudit) // rebuilt with AllowExtUnsigned
	sha := strings.Repeat("a", 64)
	stdio := pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: "codeword", Version: "1.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeStdio,
		Artifacts: map[string]pkgmeta.Artifact{"darwin-arm64": {URL: "https://gw.example/v1/artifacts/sha256/" + sha, SHA256: sha}},
		MCP:       &pkgmeta.MCP{Tools: []pkgmeta.MCPTool{{Name: "get_codeword"}}, Stdio: &pkgmeta.MCPStdio{Args: []string{"serve"}}},
		Settings: []pkgmeta.Setting{
			{Key: "salt", Label: "Salt", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeMember, Required: true, Deliver: &pkgmeta.SettingDeliver{Env: "CODEWORD_SALT"}},
			{Key: "lang", Label: "Language", Type: pkgmeta.SettingText, Scope: pkgmeta.SettingScopeMember, Deliver: &pkgmeta.SettingDeliver{Env: "CODEWORD_LANG"}},
		},
	}
	u, _ := url.Parse(endpoint.URL)
	httpPkg := pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: "search", Version: "2.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeEndpoint,
		Endpoint: &pkgmeta.Endpoint{BaseURL: endpoint.URL + "/mcp"}, Permissions: pkgmeta.Permissions{Network: []string{u.Hostname()}},
		MCP: &pkgmeta.MCP{
			Tools: []pkgmeta.MCPTool{{Name: "search"}},
			HTTP:  &pkgmeta.MCPHTTP{Auth: pkgmeta.MCPHTTPAuth{Scheme: pkgmeta.MCPAuthBearer, Setting: "token"}},
		},
		Settings: []pkgmeta.Setting{{Key: "token", Label: "API token", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeOrg, Required: true}},
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
// its settings, with the permission its transport needs.
func TestContentSaveChecksMCPReferences(t *testing.T) {
	g, host := mcpGateway(t, false)
	cases := []struct {
		requires map[string]any
		code     int
		want     string
	}{
		{map[string]any{"mcp": []map[string]any{{"name": "ghost"}}, "permissions": []string{"mcp.stdio"}}, 400, "not an MCP tool component"},
		{map[string]any{"mcp": []map[string]any{{"name": "codeword"}}}, 400, `needs the "mcp.stdio" permission`},
		{map[string]any{"mcp": []map[string]any{{"name": "codeword", "settings": []string{"nope"}}}, "permissions": []string{"mcp.stdio"}}, 400, "not one of its settings"},
		{map[string]any{"mcp": []map[string]any{{"name": "search"}}, "permissions": []string{"mcp.http:" + host}}, 201, ""},
	}
	for i, c := range cases {
		code, raw := g.json("maya", http.MethodPost, "/v1/content-components", mcpExpert(c.requires))
		if code != c.code || !strings.Contains(errorMessage(raw), c.want) {
			t.Fatalf("case %d = %d %s", i, code, raw)
		}
	}
	var d wire.ContentComponentDetail
	_, raw := g.do("maya", http.MethodGet, "/v1/content-components?kind=expert", "", nil)
	_ = json.Unmarshal(raw, &d)
	if !strings.Contains(string(raw), `"mcp":[{"name":"search"}]`) {
		t.Fatalf("the card lists its MCP tools: %s", raw)
	}
}

// The member catalog lists MCP tools with their tools and settings; a tool
// whose organization setting is not set says so (needs setup).
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
			Settings        []pkgmeta.Setting           `json:"settings"`
			SettingsMissing []string                    `json:"settingsMissing"`
			MCP             *struct {
				Transport string            `json:"transport"`
				Tools     []pkgmeta.MCPTool `json:"tools"`
			} `json:"mcp"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Components) != 2 {
		t.Fatalf("catalog = %s", raw)
	}
	for _, c := range out.Components {
		switch c.Name {
		case "codeword":
			if c.Runtime != "stdio" || !c.RequiresDesktop || c.Artifacts["darwin-arm64"].SHA256 == "" || c.MCP.Transport != "stdio" || len(c.Settings) != 2 || c.SettingsMissing != nil {
				t.Fatalf("codeword = %+v", c)
			}
		case "search":
			if c.MCP.Transport != "http" || len(c.SettingsMissing) != 1 || c.SettingsMissing[0] != "token" {
				t.Fatalf("search = %+v", c)
			}
		}
	}
}

// superAdminToken signs in a super admin of the content gateway.
func superAdminToken(t *testing.T, g *contentGateway) string {
	t.Helper()
	resolve := g.svc.TenantResolver
	g.svc.TenantResolver = func(id auth.Identity, hint string) (string, string, error) {
		if id.Subject == "root" {
			return "home", string(rbac.RoleSuperAdmin), nil
		}
		return resolve(id, hint)
	}
	g.svc.SuperAdmins = append(g.svc.SuperAdmins, "root")
	tok, _ := login(t, g.srv, "root")
	return tok
}

// The effective resolution of a job lists the MCP tools its content
// requires (with their organization values for the bridge holding the
// lease) and reports the unmet ones, one needing setup among them, as
// missing.
func TestContentEffectiveResolvesMCP(t *testing.T) {
	g, host := mcpGateway(t, true)
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components", mcpExpert(map[string]any{
		"mcp":         []map[string]any{{"name": "codeword", "settings": []string{"lang"}}, {"name": "search"}},
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
		eff.MCP[0].Artifacts["darwin-arm64"] != strings.Repeat("a", 64) || !eff.MCP[0].Settings[1].Required || eff.MCP[0].RequiredBy[0] != ex.Component.ID {
		t.Fatalf("mcp = %+v", eff.MCP)
	}
	if len(eff.Missing) != 1 || eff.Missing[0] != "MCP tool search (needs setup: an admin has not set API token)" {
		t.Fatalf("a tool needing setup is missing: %v", eff.Missing)
	}
	if code, raw := g.json("ada", http.MethodPut, "/v1/tenant/components/search/settings?kind=mcp", map[string]any{"values": map[string]string{"token": "tok"}}); code != http.StatusOK {
		t.Fatalf("put = %d %s", code, raw)
	}
	eff = effective()
	if len(eff.MCP) != 2 || len(eff.Missing) != 0 {
		t.Fatalf("mcp = %+v missing = %v", eff.MCP, eff.Missing)
	}
	s := eff.MCP[1]
	if s.Transport != "http" || !strings.HasSuffix(s.URL, "/mcp") || s.Auth.Scheme != "bearer" || s.Auth.Setting != "token" || s.Values["token"] != "tok" {
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

// An http tool declaring a member OAuth sign-in reaches the member catalog
// with its server URL and sign-in hints (the desktop signs in from them);
// it has no settings.
func TestMemberCatalogListsOAuthMCPTool(t *testing.T) {
	g, host := mcpGateway(t, true)
	pkg := pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: "drive", Version: "1.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeEndpoint,
		Endpoint: &pkgmeta.Endpoint{BaseURL: "http://" + host + "/drive"}, Permissions: pkgmeta.Permissions{Network: []string{strings.Split(host, ":")[0]}},
		MCP: &pkgmeta.MCP{
			Tools: []pkgmeta.MCPTool{{Name: "list_files"}},
			HTTP:  &pkgmeta.MCPHTTP{Auth: pkgmeta.MCPHTTPAuth{Scheme: pkgmeta.MCPAuthOAuth, ClientID: "bnbloc", Scopes: []string{"files:read"}}},
		},
	}
	raw, _ := json.Marshal(pkg)
	if _, err := g.svc.corePipeline().Install(context.Background(), extpipe.InstallRequest{Body: raw, Actor: "root"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	code, body := g.do("maya", http.MethodGet, "/v1/components?kind=mcp", "", nil)
	if code != http.StatusOK {
		t.Fatalf("catalog = %d %s", code, body)
	}
	var out struct {
		Components []struct {
			Name     string            `json:"name"`
			Endpoint *pkgmeta.Endpoint `json:"endpoint"`
			Settings []pkgmeta.Setting `json:"settings"`
			MCP      *struct {
				Transport   string           `json:"transport"`
				Credentials string           `json:"credentials"`
				HTTP        *pkgmeta.MCPHTTP `json:"http"`
			} `json:"mcp"`
		} `json:"components"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Components {
		if c.Name != "drive" {
			continue
		}
		if c.Endpoint == nil || c.Endpoint.BaseURL != "http://"+host+"/drive" || c.MCP == nil || c.MCP.Transport != "http" ||
			c.MCP.HTTP == nil || c.MCP.HTTP.Auth.Scheme != "oauth" || c.MCP.HTTP.Auth.ClientID != "bnbloc" || c.MCP.Credentials != "" || c.Settings != nil {
			t.Fatalf("drive = %s", body)
		}
		return
	}
	t.Fatalf("drive not listed: %s", body)
}

// A job whose content requires no tool still gets the tools its requester
// installed (an auto rollout installs for everyone), and not the ones they
// did not (docs/prd/2026-10-17-browser-component.md §3.6).
func TestContentEffectiveOffersInstalledMCP(t *testing.T) {
	g, _ := mcpGateway(t, true)
	g.svc.ComponentInstalls = store.NewMemoryComponentInstall()
	ctx := tenant.NewContext(context.Background(), "home")
	_ = g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()})
	job, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 3, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1})
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
	// search needs setup: offered, not attachable, not missing.
	eff := effective()
	if len(eff.MCP) != 1 || eff.MCP[0].Name != "codeword" || len(eff.MCP[0].RequiredBy) != 0 || len(eff.Missing) != 0 {
		t.Fatalf("mcp = %+v missing = %v", eff.MCP, eff.Missing)
	}
	// Opt-in: only once the requester added it.
	g.tokens["root"] = superAdminToken(t, g)
	if code, raw := g.json("root", http.MethodPut, "/v1/platform/components/codeword/rollout?kind=mcp", map[string]string{"rollout": "optin"}); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("rollout = %d %s", code, raw)
	}
	if eff = effective(); len(eff.MCP) != 0 || len(eff.Missing) != 0 {
		t.Fatalf("an opt-in tool maya did not add: mcp = %+v missing = %v", eff.MCP, eff.Missing)
	}
	if code, raw := g.do("maya", http.MethodPost, "/v1/components/codeword/install?kind=mcp", "", nil); code != http.StatusNoContent {
		t.Fatalf("install = %d %s", code, raw)
	}
	if eff = effective(); len(eff.MCP) != 1 || eff.MCP[0].Name != "codeword" {
		t.Fatalf("after maya added it: mcp = %+v missing = %v", eff.MCP, eff.Missing)
	}
}
