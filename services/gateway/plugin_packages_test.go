package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

func pluginPackageBody(t *testing.T, name, localURL string, hooks []string) []byte {
	t.Helper()
	return pluginPackageBodyWithArtifact(t, name, localURL, hooks, pkgmeta.Artifact{URL: "https://gw.example/v1/artifacts/sha256/" + missingHex, SHA256: missingHex})
}

func pluginPackageBodyWithArtifact(t *testing.T, name, localURL string, hooks []string, artifact pkgmeta.Artifact) []byte {
	t.Helper()
	body := map[string]any{
		"kind":        pkgmeta.KindPluginPackageV1,
		"name":        name,
		"version":     "0.3.1",
		"runtime":     "sidecar",
		"contract":    pkgmeta.PluginContractV1,
		"hooks":       hooks,
		"artifacts":   map[string]any{"darwin-arm64": map[string]string{"url": artifact.URL, "sha256": artifact.SHA256}},
		"sidecar":     map[string]any{"localUrl": localURL, "listenEnv": "BNBLOC_PLUGIN_LISTEN", "args": []string{"--serve"}},
		"permissions": map[string]any{"network": []string{"127.0.0.1"}},
		"publisher":   "Acme Corp",
		"description": "audits room events out of process",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal plugin package: %v", err)
	}
	return raw
}

func TestPluginPackageInstall_persistsPluginWithoutGatewayActivation(t *testing.T) {
	// Given
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("plugin sidecar bytes"))
	body := pluginPackageBodyWithArtifact(t, "room-auditor", "http://127.0.0.1:7191", []string{"chat_message.v1", "agent_messages.v1"}, artifact)
	t.Cleanup(func() { _ = extreg.Unregister(pkgmeta.KindPlugin, "room-auditor") })

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", status, respBody)
	}
	if _, ok := extreg.Get(pkgmeta.KindPlugin, "room-auditor"); ok {
		t.Fatal("gateway install activated plugin in-process; plugin hooks must activate only in realtime")
	}
	if _, ok, err := svc.PlatformSettings.GetPlatformSetting(t.Context(), "ext.plugin.room-auditor.installed"); err != nil || !ok {
		t.Fatalf("plugin record exists = %v, %v; want ext.plugin.room-auditor.installed", ok, err)
	}
	components := listComponentsForAdmin(t, srv.URL, token)
	for _, component := range components {
		if component.Name != "room-auditor" {
			continue
		}
		if component.Kind != string(pkgmeta.KindPlugin) || component.Runtime != pkgmeta.RuntimeSidecar {
			t.Fatalf("component kind/runtime = %s/%s, want plugin/sidecar", component.Kind, component.Runtime)
		}
		if len(component.Hooks) != 2 || component.Hooks[0] != "chat_message.v1" || component.Hooks[1] != "agent_messages.v1" {
			t.Fatalf("hooks = %v, want fixed plugin hooks", component.Hooks)
		}
		return
	}
	t.Fatal("plugin component missing from admin list")
}

func TestPluginPackageInstall_rejectsUnknownHookAndBadArtifactMetadata(t *testing.T) {
	cases := []struct {
		name string
		body func(t *testing.T, srv *httptest.Server, token string) []byte
		want string
	}{
		{
			name: "unknown hook",
			body: func(t *testing.T, srv *httptest.Server, token string) []byte {
				artifact := uploadComponentArtifact(t, srv.URL, token, []byte("bad hook plugin bytes"))
				return pluginPackageBodyWithArtifact(t, "bad-hook-plugin", "http://127.0.0.1:7191", []string{"presence_typing.v1"}, artifact)
			},
			want: `unknown hook "presence_typing.v1"`,
		},
		{
			name: "missing artifact",
			body: func(t *testing.T, _ *httptest.Server, _ string) []byte {
				var pkg map[string]any
				raw := pluginPackageBody(t, "missing-artifact-plugin", "http://127.0.0.1:7191", []string{"chat_message.v1"})
				if err := json.Unmarshal(raw, &pkg); err != nil {
					t.Fatalf("unmarshal plugin: %v", err)
				}
				delete(pkg, "artifacts")
				body, _ := json.Marshal(pkg)
				return body
			},
			want: "plugin sidecar requires artifacts",
		},
		{
			name: "bad hash",
			body: func(t *testing.T, _ *httptest.Server, _ string) []byte {
				var pkg map[string]any
				raw := pluginPackageBody(t, "bad-hash-plugin", "http://127.0.0.1:7191", []string{"chat_message.v1"})
				if err := json.Unmarshal(raw, &pkg); err != nil {
					t.Fatalf("unmarshal plugin: %v", err)
				}
				pkg["artifacts"] = map[string]any{"darwin-arm64": map[string]string{"url": "https://gw.example/plugin", "sha256": "not-a-sha"}}
				body, _ := json.Marshal(pkg)
				return body
			},
			want: "sha256 must be 64 lowercase hex characters",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
			body := tc.body(t, srv, token)

			// When
			respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

			// Then
			if status != http.StatusBadRequest || !strings.Contains(string(respBody), tc.want) {
				t.Fatalf("status=%d body=%s, want 400 containing %q", status, respBody, tc.want)
			}
		})
	}
}

func TestPluginPackageInstall_rejectsSignatureFailures(t *testing.T) {
	cases := []struct {
		name      string
		signature func(testKey, []byte) string
		trusted   bool
		want      string
	}{
		{name: "unsigned", signature: func(testKey, []byte) string { return "" }, trusted: true, want: "signature required"},
		{name: "untrusted", signature: func(k testKey, b []byte) string { return signPackage(k.private, b) }, trusted: false, want: "untrusted publisher key"},
		{name: "tampered", signature: func(k testKey, b []byte) string { return signPackage(k.private, b) }, trusted: true, want: "signature verification failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			_, srv, token, key := signedPackageGateway(t, tc.trusted)
			body := pluginPackageBody(t, "signed-plugin", "http://127.0.0.1:7191", []string{"chat_message.v1"})
			sentBody := body
			if tc.name == "tampered" {
				sentBody = []byte(strings.Replace(string(body), "signed-plugin", "other-plugin", 1))
			}

			// When
			respBody, status := postComponent(t, srv.URL, token, sentBody, tc.signature(key, body))

			// Then
			if status != http.StatusBadRequest || !strings.Contains(string(respBody), tc.want) {
				t.Fatalf("status=%d body=%s, want 400 containing %q", status, respBody, tc.want)
			}
		})
	}
}

func TestComponentsMemberCatalog_filtersPluginByRolloutAndOptIn(t *testing.T) {
	// Given
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	artifact := uploadComponentArtifact(t, srv.URL, adminToken, []byte("optin plugin sidecar bytes"))
	body := pluginPackageBodyWithArtifact(t, "optin-plugin", "http://127.0.0.1:7191", []string{"chat_message.v1"}, artifact)
	if respBody, status := postComponent(t, srv.URL, adminToken, body, ""); status != http.StatusOK {
		t.Fatalf("install status=%d body=%s, want 200", status, respBody)
	}
	if status := setRollout(t, srv, adminToken, "optin-plugin", "optin"); status != http.StatusNoContent {
		t.Fatalf("set optin status=%d, want 204", status)
	}

	components := listComponentsForMember(t, srv, memberToken)
	plugin, ok := components["optin-plugin"]
	if !ok || plugin.Installed || plugin.Managed {
		t.Fatalf("plugin before install = %+v, want visible available opt-in", plugin)
	}
	if status := memberInstallStatus(t, srv, memberToken, "optin-plugin", http.MethodPost); status != http.StatusNoContent {
		t.Fatalf("member install status=%d, want 204", status)
	}
	components = listComponentsForMember(t, srv, memberToken)
	if !components["optin-plugin"].Installed {
		t.Fatalf("plugin after install = %+v, want installed", components["optin-plugin"])
	}
	if status := setRollout(t, srv, adminToken, "optin-plugin", "off"); status != http.StatusNoContent {
		t.Fatalf("set off status=%d, want 204", status)
	}
	components = listComponentsForMember(t, srv, memberToken)
	if _, ok := components["optin-plugin"]; ok {
		t.Fatalf("off plugin leaked to member catalog: %+v", components["optin-plugin"])
	}
}

func TestComponentsMemberCatalog_hidesPluginLaunchMetadataUntilInstalled(t *testing.T) {
	// Given
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	artifact := uploadComponentArtifact(t, srv.URL, adminToken, []byte("private plugin sidecar bytes"))
	body := pluginPackageBodyWithArtifact(t, "private-plugin", "http://127.0.0.1:7191", []string{"chat_message.v1"}, artifact)
	if respBody, status := postComponent(t, srv.URL, adminToken, body, ""); status != http.StatusOK {
		t.Fatalf("install status=%d body=%s, want 200", status, respBody)
	}
	if status := setRollout(t, srv, adminToken, "private-plugin", "optin"); status != http.StatusNoContent {
		t.Fatalf("set optin status=%d, want 204", status)
	}

	// When
	components := rawComponentsForMember(t, srv.URL, memberToken)

	// Then
	plugin := components["private-plugin"]
	if plugin["artifacts"] != nil || plugin["sidecar"] != nil || plugin["manifest"] != nil {
		t.Fatalf("uninstalled plugin leaked launch metadata: %+v", plugin)
	}
	if status := memberInstallStatus(t, srv, memberToken, "private-plugin", http.MethodPost); status != http.StatusNoContent {
		t.Fatalf("member install status=%d, want 204", status)
	}
	plugin = rawComponentsForMember(t, srv.URL, memberToken)["private-plugin"]
	if plugin["artifacts"] == nil || plugin["sidecar"] == nil {
		t.Fatalf("installed plugin missing launch metadata: %+v", plugin)
	}
}

// The member catalog hands an installed notification plugin's host API
// permissions and local settings schema to the desktop (it launches the
// sidecar with them); the settings values never come back here.
func TestComponentsMemberCatalog_carriesPluginHostPermissionsAndLocalSettings(t *testing.T) {
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	artifact := uploadComponentArtifact(t, srv.URL, adminToken, []byte("personal bot sidecar bytes"))
	var pkg map[string]any
	_ = json.Unmarshal(pluginPackageBodyWithArtifact(t, "personal-bot", "http://127.0.0.1:7193", []string{pkgmeta.NotificationHook}, artifact), &pkg)
	pkg["permissions"] = map[string]any{"network": []string{"127.0.0.1"}, "host": []string{pkgmeta.PluginHostChat}}
	pkg["localSettings"] = []map[string]any{{"key": "appKey", "label": "App key", "secret": true, "required": true}}
	body, _ := json.Marshal(pkg)
	if respBody, status := postComponent(t, srv.URL, adminToken, body, ""); status != http.StatusOK {
		t.Fatalf("install status=%d body=%s, want 200", status, respBody)
	}
	plugin := rawComponentsForMember(t, srv.URL, memberToken)["personal-bot"]
	perms, _ := plugin["permissions"].(map[string]any)
	host, _ := perms["host"].([]any)
	settings, _ := plugin["localSettings"].([]any)
	if len(host) != 1 || host[0] != pkgmeta.PluginHostChat || len(settings) != 1 {
		t.Fatalf("member catalog entry = %+v", plugin)
	}
	if field, _ := settings[0].(map[string]any); field["key"] != "appKey" || field["secret"] != true || field["required"] != true {
		t.Fatalf("local setting = %+v", settings[0])
	}
}

func rawComponentsForMember(t *testing.T, baseURL, token string) map[string]map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/components", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list components: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Components []map[string]any `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode components: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, component := range out.Components {
		name, _ := component["name"].(string)
		byName[name] = component
	}
	return byName
}
