package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// installNotes adds "notes" to an MCP gateway: a stdio tool with a
// platform URL, an organization key and a member path.
func installNotes(t *testing.T, g *contentGateway) {
	t.Helper()
	sha := strings.Repeat("c", 64)
	pkg := pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: "notes", Version: "1.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeStdio,
		Artifacts: map[string]pkgmeta.Artifact{"darwin-arm64": {URL: "https://gw.example/v1/artifacts/sha256/" + sha, SHA256: sha}},
		MCP:       &pkgmeta.MCP{Tools: []pkgmeta.MCPTool{{Name: "list_notes"}}},
		Settings: []pkgmeta.Setting{
			{Key: "apiBase", Label: "API base URL", Type: pkgmeta.SettingURL, Scope: pkgmeta.SettingScopePlatform, Required: true, Deliver: &pkgmeta.SettingDeliver{Env: "NOTES_API_BASE"}},
			{Key: "apiKey", Label: "API key", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeOrg, Required: true, Deliver: &pkgmeta.SettingDeliver{Env: "NOTES_API_KEY"}},
			{Key: "pageSize", Label: "Page size", Type: pkgmeta.SettingNumber, Scope: pkgmeta.SettingScopeOrg, Default: "20"},
			{Key: "folder", Label: "Folder", Type: pkgmeta.SettingPath, Scope: pkgmeta.SettingScopeMember},
		},
	}
	raw, _ := json.Marshal(pkg)
	if _, err := g.svc.corePipeline().Install(context.Background(), extpipe.InstallRequest{Body: raw, Actor: "root"}); err != nil {
		t.Fatalf("install notes: %v", err)
	}
}

func settingsView(t *testing.T, raw []byte) wire.ComponentSettingsView {
	t.Helper()
	var v wire.ComponentSettingsView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("view %s: %v", raw, err)
	}
	return v
}

func missingKeys(v wire.ComponentSettingsView) []string {
	var out []string
	for _, s := range v.Missing {
		out = append(out, s.Key)
	}
	return out
}

// Each scope is set by its own admin, checked against the declaration,
// sealed at rest, a secret never shown back, every save audited by key.
func TestComponentSettingsRoutes(t *testing.T) {
	g, _ := mcpGateway(t, true)
	installNotes(t, g)
	g.tokens["root"] = superAdminToken(t, g)
	audit := &recordedAudit{}
	g.svc.ExtAudit = audit

	// Who may: a member neither scope, an org admin only their organization's.
	if code, _ := g.do("maya", http.MethodGet, "/v1/tenant/components/notes/settings?kind=mcp", "", nil); code != http.StatusForbidden {
		t.Fatalf("member org get = %d", code)
	}
	if code, _ := g.json("ada", http.MethodPut, "/v1/platform/components/notes/settings?kind=mcp", map[string]any{"values": map[string]string{"apiBase": "https://notes.example.com"}}); code != http.StatusForbidden {
		t.Fatalf("org admin platform put = %d", code)
	}
	if code, _ := g.do("ada", http.MethodGet, "/v1/tenant/components/ghost/settings", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown component = %d", code)
	}

	// The org form: its settings, and what keeps the tool in needs setup.
	code, raw := g.do("ada", http.MethodGet, "/v1/tenant/components/notes/settings?kind=mcp", "", nil)
	v := settingsView(t, raw)
	if code != http.StatusOK || v.Scope != "org" || !v.Available || !v.ReachesMembers || len(v.Fields) != 2 || v.Fields[0].Key != "apiKey" ||
		!slices.Equal(missingKeys(v), []string{"apiBase", "apiKey"}) {
		t.Fatalf("org view = %d %s", code, raw)
	}
	// Checked against the declaration.
	for _, c := range []struct {
		values map[string]string
		want   string
	}{
		{map[string]string{"apiKey": "k", "pageSize": "many"}, "Page size must be a number"},
		{map[string]string{"apiKey": "k", "apiBase": "https://x.example.com"}, `"apiBase" is not a org setting`},
		{map[string]string{"apiKey": "k", "folder": "/tmp"}, `"folder" is not a org setting`},
		{map[string]string{"pageSize": "10"}, "API key is required"},
	} {
		if code, raw := g.json("ada", http.MethodPut, "/v1/tenant/components/notes/settings?kind=mcp", map[string]any{"values": c.values}); code != http.StatusBadRequest || !strings.Contains(errorMessage(raw), c.want) {
			t.Fatalf("put %v = %d %s, want %q", c.values, code, raw, c.want)
		}
	}
	code, raw = g.json("ada", http.MethodPut, "/v1/tenant/components/notes/settings?kind=mcp", map[string]any{"values": map[string]string{"apiKey": "s3cr3t-key", "pageSize": "50"}})
	v = settingsView(t, raw)
	if code != http.StatusOK || !v.Fields[0].Set || v.Fields[0].Value != "" || v.Fields[1].Value != "50" || v.UpdatedBy != "ada" ||
		!slices.Equal(missingKeys(v), []string{"apiBase"}) {
		t.Fatalf("put = %d %s", code, raw)
	}
	if strings.Contains(string(raw), "s3cr3t-key") {
		t.Fatal("a secret value is never returned")
	}
	// An empty secret keeps the stored one; the platform's URL ends needs setup.
	if code, raw := g.json("ada", http.MethodPut, "/v1/tenant/components/notes/settings?kind=mcp", map[string]any{"values": map[string]string{"apiKey": ""}}); code != http.StatusOK {
		t.Fatalf("keep secret = %d %s", code, raw)
	}
	code, raw = g.json("root", http.MethodPut, "/v1/platform/components/notes/settings?kind=mcp", map[string]any{"values": map[string]string{"apiBase": "https://notes.example.com"}})
	if v := settingsView(t, raw); code != http.StatusOK || v.Scope != "platform" || len(v.Fields) != 1 || v.Fields[0].Value != "https://notes.example.com" || len(v.Missing) != 0 {
		t.Fatalf("platform put = %d %s", code, raw)
	}
	if _, raw := g.do("ada", http.MethodGet, "/v1/tenant/components/notes/settings?kind=mcp", "", nil); len(settingsView(t, raw).Missing) != 0 {
		t.Fatalf("set up: %s", raw)
	}
	rows, _ := g.svc.PlatformSettings.ListPlatformSettings(context.Background())
	keys := []string{}
	for _, row := range rows {
		if strings.Contains(row.Value, "s3cr3t-key") || strings.Contains(row.Value, "notes.example.com") {
			t.Fatalf("stored in clear under %s", row.Key)
		}
		if strings.HasPrefix(row.Key, "component.settings/") {
			keys = append(keys, row.Key)
		}
	}
	if !slices.Equal(keys, []string{"component.settings/org/home/mcp/notes", "component.settings/platform/mcp/notes"}) {
		t.Fatalf("keys = %v", keys)
	}
	// Audited by scope and key, never by value.
	recs := audit.take()
	var details []string
	for _, rec := range recs {
		if rec.Action == "settings-set" && rec.Name == "notes" {
			details = append(details, rec.TenantID+" "+rec.Detail)
		}
		if strings.Contains(rec.Detail, "s3cr3t") {
			t.Fatalf("a value in the audit: %+v", rec)
		}
	}
	if !slices.Equal(details, []string{"home scope=org keys=apiKey,pageSize", "home scope=org keys=", " scope=platform keys=apiBase"}) {
		t.Fatalf("audit = %v", details)
	}
	// Clear removes a stored secret; required, so refused here.
	if code, raw := g.json("ada", http.MethodPut, "/v1/tenant/components/notes/settings?kind=mcp", map[string]any{"values": map[string]string{}, "clear": []string{"apiKey"}}); code != http.StatusBadRequest || !strings.Contains(string(raw), "API key is required") {
		t.Fatalf("clear a required secret = %d %s", code, raw)
	}

	// A super admin sets another organization's acting in it.
	other, _ := json.Marshal(map[string]any{"values": map[string]string{"apiKey": "other-key"}})
	if code, raw := g.doWith("root", http.MethodPut, "/v1/tenant/components/notes/settings?kind=mcp", map[string]string{actingTenantHeader: "other"}, "application/json", other); code != http.StatusOK {
		t.Fatalf("super admin put = %d %s", code, raw)
	}
	if rec, ok, _ := g.svc.componentSettings().Get(context.Background(), "org", "other", "mcp", "notes"); !ok || rec.Values["apiKey"] != "other-key" {
		t.Fatalf("other organization = %+v %v", rec, ok)
	}
	// Uninstalling the component deletes every scope's values.
	if code, raw := g.do("root", http.MethodDelete, "/v1/platform/components/notes?kind=mcp", "", nil); code != http.StatusNoContent {
		t.Fatalf("uninstall = %d %s", code, raw)
	}
	rows, _ = g.svc.PlatformSettings.ListPlatformSettings(context.Background())
	for _, row := range rows {
		if strings.HasPrefix(row.Key, "component.settings/") {
			t.Fatalf("%s survived its component", row.Key)
		}
	}
}

// Without the secret store key nothing is stored: the form says so and a
// save answers 409.
func TestComponentSettingsOffWithoutKey(t *testing.T) {
	g, _ := mcpGateway(t, false)
	if code, raw := g.do("ada", http.MethodGet, "/v1/tenant/components/search/settings?kind=mcp", "", nil); code != http.StatusOK || settingsView(t, raw).Available {
		t.Fatalf("view without key = %d %s", code, raw)
	}
	if code, _ := g.json("ada", http.MethodPut, "/v1/tenant/components/search/settings?kind=mcp", map[string]any{"values": map[string]string{"token": "x"}}); code != http.StatusConflict {
		t.Fatalf("put without key = %d", code)
	}
}

// A member's desktop reads the platform and organization values of a
// component it runs, installed for them, secrets included; the catalog
// only says which are set.
func TestResolvedComponentSettings(t *testing.T) {
	g, _ := mcpGateway(t, true)
	installNotes(t, g)
	g.tokens["root"] = superAdminToken(t, g)
	resolved := func(path string) (int, wire.ComponentSettingsResolved) {
		t.Helper()
		code, raw := g.do("maya", http.MethodGet, path, "", nil)
		var out wire.ComponentSettingsResolved
		_ = json.Unmarshal(raw, &out)
		return code, out
	}
	if code, out := resolved("/v1/components/notes/settings/resolved?kind=mcp"); code != http.StatusOK || len(out.Values) != 0 || !slices.Equal(out.Missing, []string{"apiBase", "apiKey"}) {
		t.Fatalf("resolved before setup = %d %+v", code, out)
	}
	_, _ = g.json("root", http.MethodPut, "/v1/platform/components/notes/settings?kind=mcp", map[string]any{"values": map[string]string{"apiBase": "https://notes.example.com"}})
	_, _ = g.json("ada", http.MethodPut, "/v1/tenant/components/notes/settings?kind=mcp", map[string]any{"values": map[string]string{"apiKey": "s3cr3t-key"}})
	code, out := resolved("/v1/components/notes/settings/resolved?kind=mcp")
	if code != http.StatusOK || out.Values["apiKey"] != "s3cr3t-key" || out.Values["apiBase"] != "https://notes.example.com" || len(out.Missing) != 0 {
		t.Fatalf("resolved = %d %+v", code, out)
	}
	_, raw := g.do("maya", http.MethodGet, "/v1/components?kind=mcp", "", nil)
	if strings.Contains(string(raw), "s3cr3t-key") || !strings.Contains(string(raw), `"settingsSet":["apiBase","apiKey"]`) {
		t.Fatalf("catalog = %s", raw)
	}
	// An http tool runs elsewhere: its values reach no member's computer.
	if code, _ := resolved("/v1/components/search/settings/resolved?kind=mcp"); code != http.StatusNotFound {
		t.Fatalf("http tool resolved = %d", code)
	}
	// Not installed for the member: nothing.
	if code, raw := g.json("root", http.MethodPut, "/v1/platform/components/notes/rollout?kind=mcp", map[string]string{"rollout": "optin"}); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("rollout = %d %s", code, raw)
	}
	if code, _ := resolved("/v1/components/notes/settings/resolved?kind=mcp"); code != http.StatusNotFound {
		t.Fatalf("an opt-in component maya did not add = %d", code)
	}
}
