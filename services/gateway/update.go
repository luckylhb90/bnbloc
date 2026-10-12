package gateway

import (
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/semver"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type tauriUpdateManifest struct {
	Version   string `json:"version"`
	PubDate   string `json:"pub_date,omitempty"`
	URL       string `json:"url"`
	Signature string `json:"signature"`
	Notes     string `json:"notes,omitempty"`
	Forced    bool   `json:"forced,omitempty"`
}

func (s *Service) handleAppUpdate(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	// Session auth is required because the manifest contains admin-supplied
	// download URLs; exposing those unauthenticated could leak internal artifacts.
	cfg, err := loadUpdateConfig(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load update config failed", err)
		httpx.Error(w, "load update config failed", http.StatusInternalServerError)
		return
	}
	manifest, ok := updateManifestForRequest(cfg, updateRequest{
		Target:         r.PathValue("target"),
		CurrentVersion: r.PathValue("currentVersion"),
		ClientChannel:  requestUpdateChannel(r),
	})
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, manifest)
}

func (s *Service) handleUpdatePolicy(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	// Session auth matches the manifest endpoint: polling policy is only needed
	// after sign-in, and keeping it authenticated avoids exposing rollout state.
	cfg, err := loadUpdateConfig(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load update policy failed", err)
		httpx.Error(w, "load update policy failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cfg.Policy)
}

func (s *Service) handlePlatformUpdateConfig(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	cfg, err := loadUpdateConfig(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load update config failed", err)
		httpx.Error(w, "load update config failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cfg)
}

func (s *Service) handlePlatformUpdateConfigPut(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var cfg wire.UpdateConfig
	if !httpx.DecodeJSON(w, r, 64*1024, &cfg) {
		return
	}
	if cfg.Release.Platforms == nil {
		cfg.Release.Platforms = map[string]wire.UpdatePlatform{}
	}
	if err := validateUpdateConfig(cfg); err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveUpdateConfig(r.Context(), s.PlatformSettings, updateConfigSave{Config: cfg, UpdatedBy: claims.Subject, UpdatedAtMs: s.Now().UnixMilli()}); err != nil {
		obs.ServerError(w, "save update config failed", err)
		httpx.Error(w, "save update config failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cfg)
}

func updateManifestForRequest(cfg wire.UpdateConfig, req updateRequest) (tauriUpdateManifest, bool) {
	if !cfg.Policy.Enabled {
		return tauriUpdateManifest{}, false
	}
	if !wire.IsUpdateTarget(req.Target) {
		return tauriUpdateManifest{}, false
	}
	if req.ClientChannel != cfg.Policy.Channel {
		return tauriUpdateManifest{}, false
	}
	platform, ok := cfg.Release.Platforms[req.Target]
	if !ok || platform.URL == "" || platform.Signature == "" || cfg.Release.Version == "" {
		return tauriUpdateManifest{}, false
	}
	cmp, err := semver.Compare(req.CurrentVersion, cfg.Release.Version)
	if err != nil || cmp >= 0 {
		return tauriUpdateManifest{}, false
	}
	forced := false
	if cfg.Policy.MinSupportedVersion != "" {
		minCmp, err := semver.Compare(req.CurrentVersion, cfg.Policy.MinSupportedVersion)
		forced = err == nil && minCmp < 0
	}
	return tauriUpdateManifest{
		Version:   cfg.Release.Version,
		PubDate:   cfg.Release.PubDate,
		URL:       platform.URL,
		Signature: platform.Signature,
		Notes:     cfg.Release.Notes,
		Forced:    forced,
	}, true
}
