package gateway

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/obs"
)

// handleCoreArtifact resolves an installed desktop-managed core's content for
// the caller. Any authenticated session may call it (the desktop client
// fetches what it must install locally), scoped by the same per-tenant
// enablement overlay as /v1/cores. Two shapes:
//
//   - runtime "sidecar": {url, sha256} for the ?platform= key. The client GETs
//     url, pins the bytes to sha256, and spawns the binary.
//   - runtime "manifest": {version, manifest}. There is no binary — the client
//     writes the manifest into its local core-manifests dir for the bridge to
//     load. The platform key is accepted and ignored (data is platform-free).
//
// 404 when the core or (for sidecars) its artifact/platform is absent.
func (s *Service) handleCoreArtifact(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.PathValue("name")
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	if platform == "" {
		http.Error(w, "platform query parameter is required", http.StatusBadRequest)
		return
	}
	view, err := s.loadMemberComponents(r.Context(), claims)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		http.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	rec, ok := view.record(core.KindCore, name)
	if !ok {
		http.Error(w, "sidecar core not found", http.StatusNotFound)
		return
	}
	if !view.visibility(rec).Installed {
		http.Error(w, "core not available", http.StatusNotFound)
		return
	}
	p, err := componentFromExtRecord(rec)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		http.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	switch p.Runtime {
	case pkgmeta.RuntimeManifest:
		writeJSON(w, http.StatusOK, map[string]any{"version": p.Version, "manifest": p.Manifest})
	case pkgmeta.RuntimeSidecar:
		art, ok := p.Artifacts[platform]
		if !ok {
			http.Error(w, "core unavailable on this platform", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"url": art.URL, "sha256": art.SHA256, "version": p.Version, "sidecar": p.Sidecar, "manifest": p.Manifest})
	default:
		http.Error(w, "sidecar core not found", http.StatusNotFound)
	}
}

type setTenantsRequest struct {
	EnabledTenants []string `json:"enabledTenants"`
	AllTenants     bool     `json:"allTenants"`
}

func (s *Service) addCoreEntry(entry coreset.Entry) {
	s.coresMu.Lock()
	defer s.coresMu.Unlock()
	for i := range s.Cores {
		if s.Cores[i].Name == entry.Name {
			s.Cores[i] = entry
			return
		}
	}
	s.Cores = append(s.Cores, entry)
}

// AddEnvCores puts the env-configured cores (coreset.FromEnv) ahead of the
// installed ones activation already added. A name that is also installed
// keeps its activated entry: the installed record owns that provider, and
// both entries come from coreset.EntryFor, so the env overrides apply to it
// either way.
func (s *Service) AddEnvCores(env []coreset.Entry) {
	s.coresMu.Lock()
	defer s.coresMu.Unlock()
	installed := make(map[string]bool, len(s.Cores))
	for _, e := range s.Cores {
		installed[e.Name] = true
	}
	out := make([]coreset.Entry, 0, len(env)+len(s.Cores))
	for _, e := range env {
		if !installed[e.Name] {
			out = append(out, e)
		}
	}
	s.Cores = append(out, s.Cores...)
}

func (s *Service) dropCoreEntry(name string) {
	s.coresMu.Lock()
	defer s.coresMu.Unlock()
	out := s.Cores[:0]
	for _, entry := range s.Cores {
		if entry.Name != name {
			out = append(out, entry)
		}
	}
	s.Cores = out
}
