package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
)

type failingComponentInstallStore struct{}

func (failingComponentInstallStore) ListComponentInstalls(context.Context, string) ([]store.ComponentInstall, error) {
	return nil, errors.New("component install store failed")
}

func (failingComponentInstallStore) CountComponentInstalls(context.Context, string) (map[string]int, error) {
	return nil, errors.New("component install store failed")
}

func (failingComponentInstallStore) PutComponentInstall(context.Context, string, string, string, int64) error {
	return errors.New("component install store failed")
}

func (failingComponentInstallStore) DeleteComponentInstall(context.Context, string, string, string) error {
	return errors.New("component install store failed")
}

func TestCoresReturnsServerError_whenPersistedCoreRecordInvalid(t *testing.T) {
	// Given
	svc, srv, _, memberToken := rolloutTestGateway(t)
	if err := svc.PlatformSettings.PutPlatformSetting(t.Context(), store.PlatformSetting{
		Key:   "ext.core.bad-rollout.installed",
		Value: `{"kind":"core","name":"bad-rollout","rollout":"sideways"}`,
	}); err != nil {
		t.Fatalf("put invalid record: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores", nil)
	req.Header.Set("Authorization", "Bearer "+memberToken)

	// When
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list cores: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	// Then
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500 for invalid persisted core record", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "load components failed") {
		t.Fatalf("body=%s, want explicit load failure", raw)
	}
}

func TestComponents_rolloutAdminSurface(t *testing.T) {
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installRolloutTestCore(t, srv, adminToken, "guarded-core")

	if status := setRollout(t, srv, memberToken, "guarded-core", "off"); status != http.StatusForbidden {
		t.Fatalf("member set rollout status=%d, want 403", status)
	}
	if status := setRollout(t, srv, adminToken, "guarded-core", "sideways"); status != http.StatusBadRequest {
		t.Fatalf("invalid rollout status=%d, want 400", status)
	}

	components := listComponentsForAdmin(t, srv.URL, adminToken)
	found := false
	for _, component := range components {
		if component.Name == "guarded-core" {
			found = true
			if component.Rollout != "auto" {
				t.Fatalf("component rollout = %q, want auto default", component.Rollout)
			}
			if component.MemberInstalls != 0 || component.TotalMembers != 3 {
				t.Fatalf("adoption = %d/%d, want 0/3 before any opt-in", component.MemberInstalls, component.TotalMembers)
			}
		}
	}
	if !found {
		t.Fatal("installed component missing from admin list")
	}
}

func TestComponentsPublishPersistsRequestedRollout(t *testing.T) {
	tests := []struct {
		name    string
		rollout string
	}{
		{name: "default auto", rollout: ""},
		{name: "optin", rollout: extpipe.RolloutOptIn},
		{name: "off", rollout: extpipe.RolloutOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			svc, srv, adminToken, _ := rolloutTestGateway(t)
			componentName := "publish-" + strings.ReplaceAll(tt.name, " ", "-")
			artifact := uploadComponentArtifact(t, srv.URL, adminToken, []byte("rollout publish "+componentName))
			body := sidecarPackageBodyWithArtifact(t, componentName, artifact)
			t.Cleanup(func() { _ = svc.corePipeline().Uninstall(t.Context(), core.KindCore, componentName, "test") })

			// When
			raw, status := postComponentWithRollout(t, srv.URL, adminToken, body, "", tt.rollout)

			// Then
			if status != http.StatusOK {
				t.Fatalf("publish status=%d body=%s, want 200", status, raw)
			}
			var published installedComponent
			if err := json.Unmarshal(raw, &published); err != nil {
				t.Fatalf("decode published component: %v", err)
			}
			want := extpipe.NormalizeRollout(tt.rollout)
			if published.Rollout != want {
				t.Fatalf("published rollout = %q; want %q", published.Rollout, want)
			}
			stored, ok, err := svc.PlatformSettings.GetPlatformSetting(t.Context(), "ext.core."+componentName+".installed")
			if err != nil || !ok {
				t.Fatalf("stored record = %+v,%v,%v; want persisted record", stored, ok, err)
			}
			var rec extpipe.Record
			if err := json.Unmarshal([]byte(stored.Value), &rec); err != nil {
				t.Fatalf("decode stored record: %v", err)
			}
			if rec.Rollout != want {
				t.Fatalf("stored rollout = %q; want %q", rec.Rollout, want)
			}
		})
	}
}

func TestComponentsPublishRejectsInvalidRolloutWithoutRecord(t *testing.T) {
	// Given
	svc, srv, adminToken, _ := rolloutTestGateway(t)
	body := sidecarPackageBody(t, "invalid-rollout-component")

	// When
	raw, status := postComponentWithRollout(t, srv.URL, adminToken, body, "", "sideways")

	// Then
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "rollout must be auto, optin, or off") {
		t.Fatalf("publish status=%d body=%s, want invalid rollout 400", status, raw)
	}
	if stored, ok, err := svc.PlatformSettings.GetPlatformSetting(t.Context(), "ext.core.invalid-rollout-component.installed"); err != nil || ok {
		t.Fatalf("stored record = %+v,%v,%v; want no persisted record", stored, ok, err)
	}
}

func TestComponentsAdminList_failsClosed_whenAdoptionStoreFails(t *testing.T) {
	// Given
	svc, srv, adminToken, _ := rolloutTestGateway(t)
	installRolloutTestCore(t, srv, adminToken, "adoption-error-core")
	svc.ComponentInstalls = failingComponentInstallStore{}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/platform/components", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)

	// When
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list components: %v", err)
	}
	defer resp.Body.Close()

	// Then
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", resp.StatusCode)
	}
}

func TestComponentsAdminList_includesPluginFacts_withoutCoreDecode(t *testing.T) {
	// Given
	svc, srv, adminToken, _ := rolloutTestGateway(t)
	pluginConfig := map[string]any{
		"runtime":      "sidecar",
		"capabilities": []string{"room-events"},
		"permissions":  map[string]any{"network": []string{"127.0.0.1"}},
		"hooks":        []string{"chat_message.v1"},
		"artifacts": map[string]any{
			"darwin-arm64": map[string]string{"url": "https://gw.example/plugin", "sha256": missingHex},
		},
		"sidecar": map[string]string{"localUrl": "http://127.0.0.1:7098", "listenEnv": "BNBLOC_PLUGIN_LISTEN"},
	}
	rawConfig, err := json.Marshal(pluginConfig)
	if err != nil {
		t.Fatalf("marshal plugin config: %v", err)
	}
	record := map[string]any{
		"kind":          "plugin",
		"name":          "audit-plugin",
		"version":       "0.1.0",
		"publisher":     "Acme Corp",
		"keyId":         "key-1",
		"contentHash":   missingHex,
		"installedAtMs": int64(100),
		"enabled":       true,
		"rollout":       "auto",
		"config":        json.RawMessage(rawConfig),
	}
	rawRecord, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal plugin record: %v", err)
	}
	if err := svc.PlatformSettings.PutPlatformSetting(t.Context(), store.PlatformSetting{Key: "ext.plugin.audit-plugin.installed", Value: string(rawRecord)}); err != nil {
		t.Fatalf("put plugin record: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/platform/components", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)

	// When
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list components: %v", err)
	}
	defer resp.Body.Close()

	// Then
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s, want 200", resp.StatusCode, raw)
	}
	var out struct {
		Components []installedComponent `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode components: %v", err)
	}
	for _, component := range out.Components {
		if component.Name != "audit-plugin" {
			continue
		}
		if component.Kind != "plugin" || component.Runtime != pkgmeta.RuntimeSidecar {
			t.Fatalf("component kind/runtime = %s/%s, want plugin/sidecar", component.Kind, component.Runtime)
		}
		if len(component.Hooks) != 1 || component.Hooks[0] != "chat_message.v1" {
			t.Fatalf("hooks = %v, want chat_message.v1", component.Hooks)
		}
		if component.Trust.KeyID != "key-1" || !component.Trust.Signed || component.Trust.ContentHash != missingHex {
			t.Fatalf("trust = %+v, want key/hash/signed facts", component.Trust)
		}
		if len(component.Capabilities) != 1 || component.Capabilities[0] != "room-events" {
			t.Fatalf("capabilities = %v, want plugin capability", component.Capabilities)
		}
		if got := component.Permissions.Network; len(got) != 1 || got[0] != "127.0.0.1" {
			t.Fatalf("permissions = %+v, want network permission", component.Permissions)
		}
		return
	}
	t.Fatal("plugin component missing from admin list")
}

func TestComponents_unauthenticated(t *testing.T) {
	_, srv, _, _ := rolloutTestGateway(t)
	resp, err := http.Get(srv.URL + "/v1/components")
	if err != nil {
		t.Fatalf("get components: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
