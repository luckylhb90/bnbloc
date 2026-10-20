package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The example settings page (core/pkgmeta/testdata/settings-page), packed as
// a publisher would.
func settingsPageBundle(t *testing.T) []byte {
	t.Helper()
	raw, err := tarbundle.Pack("../../core/pkgmeta/testdata/settings-page", tarbundle.PreviewerLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// settingsPageGateway is the rollout gateway with an organization admin
// ("ada") beside the super admin and the member.
func settingsPageGateway(t *testing.T) (*Service, *httptest.Server, map[string]string) {
	t.Helper()
	svc, srv, adminToken, memberToken := rolloutTestGateway(t)
	resolve := svc.TenantResolver
	svc.TenantResolver = func(id auth.Identity, hint string) (string, string, error) {
		if id.Subject == "ada" {
			return "home", string(rbac.RoleAdmin), nil
		}
		return resolve(id, hint)
	}
	box, err := sealbox.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc.SecretBox = box
	adaToken, _ := login(t, srv, "ada")
	return svc, srv, map[string]string{"admin": adminToken, "member": memberToken, "ada": adaToken}
}

// notesWithPage is a stdio MCP tool with a platform and an organization
// setting and its own settings page.
func notesWithPage(t *testing.T, binary, page pkgmeta.Artifact, entry string) []byte {
	t.Helper()
	raw, err := json.Marshal(pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: "notes", Version: "1.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeStdio,
		Artifacts: map[string]pkgmeta.Artifact{"darwin-arm64": binary},
		MCP:       &pkgmeta.MCP{Tools: []pkgmeta.MCPTool{{Name: "list_notes"}}},
		Settings: []pkgmeta.Setting{
			{Key: "apiBase", Label: "API base URL", Type: pkgmeta.SettingURL, Scope: pkgmeta.SettingScopePlatform, Required: true},
			{Key: "apiKey", Label: "API key", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeOrg, Required: true},
			{Key: "pageSize", Label: "Page size", Type: pkgmeta.SettingNumber, Scope: pkgmeta.SettingScopeOrg},
		},
		SettingsPage: &pkgmeta.SettingsPage{Entry: entry, Bundle: page},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func installNotesWithPage(t *testing.T, srv *httptest.Server, adminToken string) pkgmeta.Artifact {
	t.Helper()
	binary := uploadComponentArtifact(t, srv.URL, adminToken, []byte("notes binary"))
	page := uploadComponentArtifact(t, srv.URL, adminToken, settingsPageBundle(t))
	if raw, status := postComponentWithRollout(t, srv.URL, adminToken, notesWithPage(t, binary, page, "index.html"), "", "optin"); status != http.StatusOK {
		t.Fatalf("install = %d %s", status, raw)
	}
	return page
}

func pageOf(t *testing.T, srv *httptest.Server, token, owner string) *wire.ComponentSettingsPageFrame {
	t.Helper()
	code, raw := call(t, http.MethodGet, srv.URL+"/v1/"+owner+"/components/notes/settings?kind=mcp", token, nil)
	if code != http.StatusOK {
		t.Fatalf("%s view = %d %s", owner, code, raw)
	}
	return settingsView(t, raw).Page
}

// A page whose bundle is not uploaded, is not an archive, or lacks its
// entry never installs.
func TestSettingsPage_installProbesTheBundle(t *testing.T) {
	_, srv, tokens := settingsPageGateway(t)
	binary := uploadComponentArtifact(t, srv.URL, tokens["admin"], []byte("notes binary"))
	page := uploadComponentArtifact(t, srv.URL, tokens["admin"], settingsPageBundle(t))
	notArchive := uploadComponentArtifact(t, srv.URL, tokens["admin"], []byte("<!doctype html> not an archive"))
	for name, c := range map[string]struct {
		page  pkgmeta.Artifact
		entry string
		want  string
	}{
		"placeholder":   {pkgmeta.Artifact{}, "index.html", pkgmeta.SettingsPageBundle + " must be uploaded"},
		"not a bundle":  {notArchive, "index.html", pkgmeta.SettingsPageBundle},
		"missing entry": {page, "settings.html", "settings.html"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, status := postComponent(t, srv.URL, tokens["admin"], notesWithPage(t, binary, c.page, c.entry), "")
			if status != http.StatusBadRequest || !strings.Contains(errorMessage(raw), c.want) {
				t.Fatalf("install = %d %s, want 400 %q", status, raw, c.want)
			}
		})
	}
}

// Each admin's view frames the page for their scope; the frame serves the
// bundle's files with the previewer's sandbox policy; the catalogs list the
// page, and an organization admin may download its bundle before opting in.
func TestSettingsPage_framedForEachScopeWithTheSandboxPolicy(t *testing.T) {
	_, srv, tokens := settingsPageGateway(t)
	bundle := installNotesWithPage(t, srv, tokens["admin"])

	for owner, token := range map[string]string{"platform": tokens["admin"], "tenant": tokens["ada"]} {
		page := pageOf(t, srv, token, owner)
		if page == nil || page.Entry != "index.html" || page.BundleSHA256 != bundle.SHA256 ||
			!strings.HasPrefix(page.FrameURL, "/v1/settings-pages/mcp/notes/1.0.0/") || !strings.HasSuffix(page.FrameURL, "/") || page.FrameExpiresAtMs == 0 {
			t.Fatalf("%s page = %+v", owner, page)
		}
		resp, body := getFrameFile(t, srv, page.FrameURL+page.Entry)
		want, _ := os.ReadFile("../../core/pkgmeta/testdata/settings-page/index.html")
		if resp.StatusCode != http.StatusOK || body != string(want) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s entry = %d %q", owner, resp.StatusCode, body)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self' " + srv.URL + "/v1/settings-pages/mcp/notes/1.0.0/", "connect-src 'none'", "frame-ancestors 'self'"} {
			if !strings.Contains(csp, want) {
				t.Fatalf("csp %q lacks %q", csp, want)
			}
		}
		if csp != frameCSP(srv.URL+"/v1/settings-pages/mcp/notes/1.0.0/") || resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("headers = %+v", resp.Header)
		}
		if resp, _ := getFrameFile(t, srv, page.FrameURL+"page.js"); resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
			t.Fatalf("%s script = %d", owner, resp.StatusCode)
		}
	}

	// Listed with the component, in both catalogs.
	_, raw := call(t, http.MethodGet, srv.URL+"/v1/components?kind=mcp", tokens["member"], nil)
	var catalog wire.ComponentsReply
	if err := json.Unmarshal(raw, &catalog); err != nil || len(catalog.Components) != 1 || catalog.Components[0].SettingsPage == nil ||
		catalog.Components[0].SettingsPage.Bundle != bundle {
		t.Fatalf("member catalog = %s", raw)
	}
	for _, c := range listComponentsForAdmin(t, srv.URL, tokens["admin"]) {
		if c.Name == "notes" && (c.SettingsPage == nil || c.SettingsPage.Bundle != bundle) {
			t.Fatalf("platform list page = %+v", c.SettingsPage)
		}
	}
	// The desktop serves the page itself, downloading the bundle with the
	// viewer's session: an organization admin before anyone opted in; a
	// member too (it is listed to them), never the tool's binaries.
	if got := getArtifactStatus(t, srv, tokens["ada"], bundle); got != http.StatusOK {
		t.Fatalf("org admin bundle download = %d", got)
	}
	if got := getArtifactStatus(t, srv, tokens["member"], bundle); got != http.StatusOK {
		t.Fatalf("member bundle download = %d", got)
	}
}

// The ticket reads only this page's files at this version for a scope its
// holder may still set; everything else is a 404.
func TestSettingsPage_frameRefusals(t *testing.T) {
	svc, srv, tokens := settingsPageGateway(t)
	installNotesWithPage(t, srv, tokens["admin"])
	base := pageOf(t, srv, tokens["ada"], "tenant").FrameURL
	ticket := strings.TrimSuffix(strings.TrimPrefix(base, "/v1/settings-pages/mcp/notes/1.0.0/"), "/")
	mint := func(token, kind, id string, ttl time.Duration) string {
		c, err := svc.Signer.Verify(token)
		if err != nil {
			t.Fatal(err)
		}
		c.Kind, c.ID, c.ExpiresAtUnix = kind, id, time.Now().Add(ttl).Unix()
		return svc.Signer.Issue(c)
	}
	page := func(ticket string) string { return "/v1/settings-pages/mcp/notes/1.0.0/" + ticket + "/index.html" }
	for name, path := range map[string]string{
		"unknown file":       base + "missing.js",
		"traversal":          base + "..%2findex.html",
		"not a ticket":       page("not-a-ticket"),
		"a session":          page(tokens["ada"]),
		"another version":    "/v1/settings-pages/mcp/notes/2.0.0/" + ticket + "/index.html",
		"another component":  "/v1/settings-pages/mcp/other/1.0.0/" + ticket + "/index.html",
		"another kind":       "/v1/settings-pages/plugin/notes/1.0.0/" + ticket + "/index.html",
		"expired":            page(mint(tokens["ada"], auth.KindSettingsPageFrame, "org:mcp/notes@1.0.0", -time.Minute)),
		"a previewer ticket": page(mint(tokens["ada"], auth.KindPreviewerFrame, "org:mcp/notes@1.0.0", time.Hour)),
		"the member scope":   page(mint(tokens["ada"], auth.KindSettingsPageFrame, "member:mcp/notes@1.0.0", time.Hour)),
		"a member's org":     page(mint(tokens["member"], auth.KindSettingsPageFrame, "org:mcp/notes@1.0.0", time.Hour)),
		"an org's platform":  page(mint(tokens["ada"], auth.KindSettingsPageFrame, "platform:mcp/notes@1.0.0", time.Hour)),
	} {
		if resp, _ := getFrameFile(t, srv, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %s = %d, want 404", name, path, resp.StatusCode)
		}
	}
	// Uninstalled: the ticket reads nothing more.
	if code := adminCall(t, srv, tokens["admin"], http.MethodDelete, "/v1/platform/components/notes?kind=mcp", ""); code != http.StatusNoContent {
		t.Fatalf("uninstall = %d", code)
	}
	if resp, _ := getFrameFile(t, srv, base+"index.html"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after uninstall = %d", resp.StatusCode)
	}
}

// A refused save names the setting it is about, for a page (or the form)
// to show the reason at that field.
func TestComponentSettingsRefusalNamesTheField(t *testing.T) {
	_, srv, tokens := settingsPageGateway(t)
	installNotesWithPage(t, srv, tokens["admin"])
	code, raw := call(t, http.MethodPut, srv.URL+"/v1/tenant/components/notes/settings?kind=mcp", tokens["ada"],
		wire.ComponentSettingsUpdate{Values: map[string]string{"apiKey": "k", "pageSize": "many"}})
	var body httpx.ErrorBody
	if err := json.Unmarshal(raw, &body); err != nil || code != http.StatusBadRequest || body.Field != "pageSize" || body.Error != "Page size must be a number, such as 42 or 0.5" {
		t.Fatalf("refusal = %d %s", code, raw)
	}
}
