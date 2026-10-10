package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The release hub renders bnbloc's own version history. It is stored in the
// shared platform_settings table (no dedicated table): each published version
// is one "release.history.<version>" row whose value is the JSON record, and
// the human-facing project intro rides on "release.hub.*" keys. This mirrors
// how installed core packages persist a list in the same store, so the hub
// reuses that seam instead of adding a schema.
const (
	releaseHistoryKeyPrefix = "release.history."
	releaseHubNameKey       = "release.hub.name"
	releaseHubTaglineKey    = "release.hub.tagline"
	releaseHubIntroKey      = "release.hub.intro"
)

func releaseHistoryKey(version string) string {
	return releaseHistoryKeyPrefix + version
}

// loadReleaseHub reads the intro + every stored version, newest-first.
func loadReleaseHub(ctx context.Context, settings store.PlatformSettingsStore) (wire.ReleaseHub, error) {
	hub := wire.ReleaseHub{Releases: []wire.ReleaseEntry{}}
	if settings == nil {
		return hub, nil
	}
	rows, err := settings.ListPlatformSettings(ctx)
	if err != nil {
		return wire.ReleaseHub{}, err
	}
	for _, row := range rows {
		switch row.Key {
		case releaseHubNameKey:
			hub.Project.Name = row.Value
		case releaseHubTaglineKey:
			hub.Project.Tagline = row.Value
		case releaseHubIntroKey:
			hub.Project.Intro = row.Value
		}
		if strings.HasPrefix(row.Key, releaseHistoryKeyPrefix) {
			var entry wire.ReleaseEntry
			if err := json.Unmarshal([]byte(row.Value), &entry); err != nil {
				return wire.ReleaseHub{}, err
			}
			hub.Releases = append(hub.Releases, entry)
		}
	}
	sortReleasesNewestFirst(hub.Releases)
	return hub, nil
}

// sortReleasesNewestFirst orders by semver descending, falling back to string
// compare when a version is not parseable so a bad entry never panics.
func sortReleasesNewestFirst(entries []wire.ReleaseEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		cmp, err := compareSemver(entries[i].Version, entries[j].Version)
		if err != nil {
			return entries[i].Version > entries[j].Version
		}
		return cmp > 0
	})
}

func saveReleaseEntry(ctx context.Context, settings store.PlatformSettingsStore, entry wire.ReleaseEntry, by string, atMs int64) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return settings.PutPlatformSetting(ctx, store.PlatformSetting{
		Key: releaseHistoryKey(entry.Version), Value: string(raw), UpdatedAtMs: atMs, UpdatedBy: by,
	})
}

func saveReleaseHubMeta(ctx context.Context, settings store.PlatformSettingsStore, meta wire.ReleaseHubMeta, by string, atMs int64) error {
	for key, value := range map[string]string{
		releaseHubNameKey:    meta.Name,
		releaseHubTaglineKey: meta.Tagline,
		releaseHubIntroKey:   meta.Intro,
	} {
		if err := settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: key, Value: value, UpdatedAtMs: atMs, UpdatedBy: by}); err != nil {
			return err
		}
	}
	return nil
}

func validateReleaseEntry(entry wire.ReleaseEntry) error {
	if _, err := parseSemver(entry.Version); err != nil {
		return fmt.Errorf("release version must be valid semver")
	}
	if entry.Channel != wire.UpdateChannelStable && entry.Channel != wire.UpdateChannelInternal {
		return fmt.Errorf("release channel must be stable or internal")
	}
	if entry.ReleaseDate != "" {
		if _, err := time.Parse(time.RFC3339, entry.ReleaseDate); err != nil {
			return fmt.Errorf("release date must be RFC3339")
		}
	}
	for _, f := range entry.Files {
		if strings.TrimSpace(f.URL) == "" {
			return fmt.Errorf("each file needs a url")
		}
		if err := validateHTTPURL(f.URL); err != nil {
			return fmt.Errorf("file url must be an absolute http(s) URL")
		}
	}
	return nil
}

// handleAppReleases is the public, session-authenticated hub payload: any
// signed-in user can browse/download. Auth matches the updater endpoints (the
// download URLs are admin-supplied and should not be exposed unauthenticated).
func (s *Service) handleAppReleases(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticate(r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hub, err := loadReleaseHub(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load releases failed", err)
		http.Error(w, "load releases failed", http.StatusInternalServerError)
		return
	}
	// Deployment without curated release history: BNBLOC_DESKTOP_DOWNLOAD_URL
	// still activates the UI's download affordances (one synthetic release).
	if len(hub.Releases) == 0 {
		if url := strings.TrimSpace(config.FromEnvRaw("BNBLOC_DESKTOP_DOWNLOAD_URL")); url != "" {
			hub.Releases = []wire.ReleaseEntry{{Version: "latest", Files: []wire.ReleaseFile{{Platform: "any", Arch: "any", URL: url, Name: "bnbloc-desktop"}}}}
		}
	}
	writeJSON(w, http.StatusOK, hub)
}

// handlePlatformReleases is the super-admin editor read: full history + intro.
func (s *Service) handlePlatformReleases(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	hub, err := loadReleaseHub(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load releases failed", err)
		http.Error(w, "load releases failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, hub)
}

// handlePlatformReleaseMetaPut updates the project intro (name/tagline/intro).
func (s *Service) handlePlatformReleaseMetaPut(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireSuperAdminClaims(w, r)
	if !ok {
		return
	}
	var meta wire.ReleaseHubMeta
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&meta); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if s.PlatformSettings == nil {
		obs.ServerError(w, "platform settings store not configured", nil)
		http.Error(w, "platform settings store not configured", http.StatusInternalServerError)
		return
	}
	if err := saveReleaseHubMeta(r.Context(), s.PlatformSettings, meta, claims.Subject, s.Now().UnixMilli()); err != nil {
		obs.ServerError(w, "save release meta failed", err)
		http.Error(w, "save release meta failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// handlePlatformReleasePut creates or updates one version entry.
func (s *Service) handlePlatformReleasePut(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireSuperAdminClaims(w, r)
	if !ok {
		return
	}
	var entry wire.ReleaseEntry
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&entry); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	entry.Version = strings.TrimSpace(r.PathValue("version"))
	if entry.Files == nil {
		entry.Files = []wire.ReleaseFile{}
	}
	if err := validateReleaseEntry(entry); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.PlatformSettings == nil {
		obs.ServerError(w, "platform settings store not configured", nil)
		http.Error(w, "platform settings store not configured", http.StatusInternalServerError)
		return
	}
	entry.CreatedAtMs = s.Now().UnixMilli()
	entry.CreatedBy = claims.Subject
	if err := saveReleaseEntry(r.Context(), s.PlatformSettings, entry, claims.Subject, entry.CreatedAtMs); err != nil {
		obs.ServerError(w, "save release failed", err)
		http.Error(w, "save release failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handlePlatformReleaseDelete removes a version entry. Deletion here is a hard
// delete of the settings row; a dev instance has no external state to preserve,
// and re-publishing the version simply writes it again.
func (s *Service) handlePlatformReleaseDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	version := strings.TrimSpace(r.PathValue("version"))
	if version == "" {
		http.Error(w, "version required", http.StatusBadRequest)
		return
	}
	if s.PlatformSettings == nil {
		obs.ServerError(w, "platform settings store not configured", nil)
		http.Error(w, "platform settings store not configured", http.StatusInternalServerError)
		return
	}
	if err := s.PlatformSettings.DeletePlatformSetting(r.Context(), releaseHistoryKey(version)); err != nil {
		obs.ServerError(w, "delete release failed", err)
		http.Error(w, "delete release failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
