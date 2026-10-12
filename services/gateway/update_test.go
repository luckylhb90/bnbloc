package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func testUpdateConfig() wire.UpdateConfig {
	return wire.UpdateConfig{
		Policy: wire.UpdatePolicy{
			Enabled:             true,
			Channel:             wire.UpdateChannelStable,
			AutoCheck:           true,
			CheckIntervalHours:  24,
			AutoInstall:         false,
			MinSupportedVersion: "0.1.5",
		},
		Release: wire.UpdateRelease{
			Version: "0.1.10",
			Notes:   "Release notes",
			PubDate: "2026-09-19T00:00:00Z",
			Platforms: map[string]wire.UpdatePlatform{
				"darwin-aarch64": {URL: "https://updates.example/bnbloc.app.tar.gz", Signature: "sig"},
			},
		},
	}
}

func TestUpdateManifestForRequest_whenDecisionMatrix(t *testing.T) {
	cfg := testUpdateConfig()
	tests := []struct {
		name           string
		mutate         func(*wire.UpdateConfig)
		target         string
		currentVersion string
		channel        string
		wantOffer      bool
		wantForced     bool
	}{
		{
			name:           "disabled returns no update",
			mutate:         func(c *wire.UpdateConfig) { c.Policy.Enabled = false },
			target:         "darwin-aarch64",
			currentVersion: "0.1.0",
			channel:        wire.UpdateChannelStable,
		},
		{
			name:           "current version returns no update",
			target:         "darwin-aarch64",
			currentVersion: "0.1.10",
			channel:        wire.UpdateChannelStable,
		},
		{
			name:           "newer client returns no update",
			target:         "darwin-aarch64",
			currentVersion: "0.1.11",
			channel:        wire.UpdateChannelStable,
		},
		{
			name:           "unknown target returns no update",
			target:         "linux-x86_64",
			currentVersion: "0.1.0",
			channel:        wire.UpdateChannelStable,
		},
		{
			name:           "older stable client receives stable update",
			target:         "darwin-aarch64",
			currentVersion: "0.1.9",
			channel:        wire.UpdateChannelStable,
			wantOffer:      true,
		},
		{
			name:           "below minimum is forced",
			target:         "darwin-aarch64",
			currentVersion: "0.1.4",
			channel:        wire.UpdateChannelStable,
			wantOffer:      true,
			wantForced:     true,
		},
		{
			name:           "stable client never receives internal update",
			mutate:         func(c *wire.UpdateConfig) { c.Policy.Channel = wire.UpdateChannelInternal },
			target:         "darwin-aarch64",
			currentVersion: "0.1.0",
			channel:        wire.UpdateChannelStable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			c.Release.Platforms = map[string]wire.UpdatePlatform{"darwin-aarch64": cfg.Release.Platforms["darwin-aarch64"]}
			if tc.mutate != nil {
				tc.mutate(&c)
			}

			manifest, ok := updateManifestForRequest(c, updateRequest{Target: tc.target, CurrentVersion: tc.currentVersion, ClientChannel: tc.channel})

			if ok != tc.wantOffer {
				t.Fatalf("offer = %v, want %v", ok, tc.wantOffer)
			}
			if manifest.Forced != tc.wantForced {
				t.Fatalf("forced = %v, want %v", manifest.Forced, tc.wantForced)
			}
		})
	}
}

func TestValidateUpdateConfig_whenInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*wire.UpdateConfig)
		want   string
	}{
		{name: "bad channel", mutate: func(c *wire.UpdateConfig) { c.Policy.Channel = "beta" }, want: "updates.channel"},
		{name: "bad interval", mutate: func(c *wire.UpdateConfig) { c.Policy.CheckIntervalHours = 0 }, want: "check_interval_hours"},
		{name: "interval beyond max", mutate: func(c *wire.UpdateConfig) { c.Policy.CheckIntervalHours = maxCheckIntervalHours + 1 }, want: "check_interval_hours"},
		{name: "bad release semver", mutate: func(c *wire.UpdateConfig) { c.Release.Version = "0.1" }, want: "release.version"},
		{name: "bad min semver", mutate: func(c *wire.UpdateConfig) { c.Policy.MinSupportedVersion = "soon" }, want: "min_supported_version"},
		{name: "bad pub date", mutate: func(c *wire.UpdateConfig) { c.Release.PubDate = "yesterday" }, want: "release.pub_date"},
		{name: "bad url", mutate: func(c *wire.UpdateConfig) {
			c.Release.Platforms["darwin-aarch64"] = wire.UpdatePlatform{URL: "/local", Signature: "sig"}
		}, want: "release.darwin-aarch64.url"},
		{name: "url without signature", mutate: func(c *wire.UpdateConfig) {
			c.Release.Platforms["darwin-aarch64"] = wire.UpdatePlatform{URL: "https://updates.example/app"}
		}, want: "must be set together"},
		{name: "bad target", mutate: func(c *wire.UpdateConfig) {
			c.Release.Platforms["linux-x86_64"] = wire.UpdatePlatform{URL: "https://updates.example/linux", Signature: "sig"}
		}, want: "unsupported release target"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testUpdateConfig()
			tc.mutate(&cfg)

			err := validateUpdateConfig(cfg)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestAppUpdateRoute_whenUpdateDecisionMatrix(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(*wire.UpdateConfig)
		currentVersion string
		wantStatus     int
	}{
		{name: "disabled", mutate: func(c *wire.UpdateConfig) { c.Policy.Enabled = false }, currentVersion: "0.1.0", wantStatus: http.StatusNoContent},
		{name: "already current", currentVersion: "0.1.10", wantStatus: http.StatusNoContent},
		{name: "older version", currentVersion: "0.1.9", wantStatus: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			settings := store.NewMemoryPlatformSettings()
			cfg := testUpdateConfig()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			if err := saveUpdateConfig(context.Background(), settings, updateConfigSave{Config: cfg, UpdatedBy: "root", UpdatedAtMs: 1}); err != nil {
				t.Fatalf("save config: %v", err)
			}
			svc := newGateway(t, "")
			svc.PlatformSettings = settings
			srv := httptest.NewServer(svc.Handler())
			defer srv.Close()
			token, _ := login(t, srv, "good")

			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/app/update/darwin-aarch64/"+tc.currentVersion, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("get update: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestAppUpdateRoute_whenUnregisteredPathWouldHitSPAFallback(t *testing.T) {
	settings := store.NewMemoryPlatformSettings()
	if err := saveUpdateConfig(context.Background(), settings, updateConfigSave{Config: testUpdateConfig(), UpdatedBy: "root", UpdatedAtMs: 1}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	svc := newGateway(t, "")
	svc.PlatformSettings = settings
	svc.WebUI = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>spa</html>")
	})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/app/update/darwin-aarch64/0.1.9", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get update: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%q, want 200 manifest", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("content-type = %q, want json", resp.Header.Get("Content-Type"))
	}
	if strings.Contains(string(body), "<html>") {
		t.Fatalf("update route served SPA fallback: %q", body)
	}
}

func TestUpdatePolicyRoute_whenAuthenticated(t *testing.T) {
	settings := store.NewMemoryPlatformSettings()
	cfg := testUpdateConfig()
	cfg.Policy.Enabled = false
	cfg.Policy.AutoCheck = false
	cfg.Policy.CheckIntervalHours = 6
	if err := saveUpdateConfig(context.Background(), settings, updateConfigSave{Config: cfg, UpdatedBy: "root", UpdatedAtMs: 1}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	svc := newGateway(t, "")
	svc.PlatformSettings = settings
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/app/update-policy", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	defer resp.Body.Close()
	var got wire.UpdatePolicy
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode policy: %v", err)
	}

	if resp.StatusCode != http.StatusOK || got.Enabled || got.AutoCheck || got.CheckIntervalHours != 6 {
		t.Fatalf("status=%d policy=%+v, want disabled polling policy", resp.StatusCode, got)
	}
}

func TestAppUpdateRoute_whenUnauthenticated(t *testing.T) {
	svc := newGateway(t, "")
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/app/update/darwin-aarch64/0.1.0")
	if err != nil {
		t.Fatalf("get update: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestPlatformUpdateConfigPut_whenRoleIsNotSuperAdmin(t *testing.T) {
	svc := newGateway(t, "")
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", "org_admin", nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")
	body, _ := json.Marshal(testUpdateConfig())
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/platform/update-config", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestPlatformUpdateConfigPut_whenRoleIsSuperAdmin(t *testing.T) {
	settings := store.NewMemoryPlatformSettings()
	svc := newGateway(t, "")
	svc.PlatformSettings = settings
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", "super_admin", nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")
	body, _ := json.Marshal(testUpdateConfig())
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/platform/update-config", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%q, want 200", resp.StatusCode, raw)
	}
	got, ok, err := settings.GetPlatformSetting(context.Background(), updateKeyReleaseVersion)
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if !ok || got.Value != "0.1.10" || got.UpdatedBy != "alice" {
		t.Fatalf("stored release version = %+v,%v want root-authored config", got, ok)
	}
}
