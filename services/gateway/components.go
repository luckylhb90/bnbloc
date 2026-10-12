package gateway

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type componentInfo struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Publisher       string   `json:"publisher,omitempty"`
	Description     string   `json:"description,omitempty"`
	Runtime         string   `json:"runtime,omitempty"`
	Contract        string   `json:"contract,omitempty"`
	RequiresDesktop bool     `json:"requiresDesktop,omitempty"`
	Hooks           []string `json:"hooks,omitempty"`
	// Permissions and LocalSettings tell the member's desktop how to launch
	// a plugin sidecar: the host API capabilities it may use, and the
	// settings the member fills in locally (never sent back here).
	Permissions   *pkgmeta.Permissions        `json:"permissions,omitempty"`
	LocalSettings []pkgmeta.LocalSetting      `json:"localSettings,omitempty"`
	Artifacts     map[string]pkgmeta.Artifact `json:"artifacts,omitempty"`
	Endpoint      *pkgmeta.Endpoint           `json:"endpoint,omitempty"`
	Sidecar       *pkgmeta.Sidecar            `json:"sidecar,omitempty"`
	// Capabilities and Protocol tell the desktop/bridge what a protocol
	// component (a sandbox runtime) was published to support before it is
	// launched; the running implementation's discovery stays authoritative.
	Capabilities     []string                `json:"capabilities,omitempty"`
	Protocol         *protocol.ProtocolRange `json:"protocol,omitempty"`
	Rollout          string                  `json:"rollout"`
	Installed        bool                    `json:"installed"`
	Managed          bool                    `json:"managed"`
	InstalledAtMs    int64                   `json:"installedAtMs,omitempty"`
	MembersInstalled int                     `json:"membersInstalled,omitempty"`
	// Guest is a sandbox's machine profile: the desktop prewarms it.
	Guest *pkgmeta.Guest `json:"guest,omitempty"`
	// Previewer is an installed previewer's match/toolbar metadata, its
	// pinned bundle, and the caller's browser frame URL (previewers.go).
	Previewer *previewerCatalogInfo `json:"previewer,omitempty"`
}

func (s *Service) handleComponents(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	recs, err := s.corePipeline().List(r.Context(), "")
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		http.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	installedBy, err := s.memberInstalledComponents(r.Context(), claims.Subject)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		http.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	memberCounts := map[string]int{}
	if s.ComponentInstalls != nil {
		seenKinds := map[string]bool{}
		for _, rec := range recs {
			kind := rec.Kind
			if seenKinds[kind] {
				continue
			}
			seenKinds[kind] = true
			counts, err := s.ComponentInstalls.CountComponentInstalls(r.Context(), kind)
			if err != nil {
				obs.ServerError(w, "load components failed", err)
				http.Error(w, "load components failed", http.StatusInternalServerError)
				return
			}
			for name, count := range counts {
				memberCounts[componentKey(kind, name)] = count
			}
		}
	}
	// ?kind= narrows the catalog to one kind (e.g. the web host reads only
	// previewers); absent lists every member-visible kind.
	onlyKind := strings.TrimSpace(r.URL.Query().Get("kind"))
	out := make([]componentInfo, 0, len(recs))
	for _, rec := range recs {
		if !componentKindVisibleInMemberCatalog(componentKindForName(rec)) || (onlyKind != "" && rec.Kind != onlyKind) {
			continue
		}
		vis := extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForRecord(rec), installedBy[componentKey(rec.Kind, rec.Name)])
		if !vis.Visible {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			obs.ServerError(w, "load components failed", err)
			http.Error(w, "load components failed", http.StatusInternalServerError)
			return
		}
		component := componentInfo{
			Kind:             rec.Kind,
			Name:             rec.Name,
			Version:          rec.Version,
			Publisher:        rec.Publisher,
			Description:      cfg.Description,
			Runtime:          string(cfg.Runtime),
			Contract:         cfg.Contract,
			RequiresDesktop:  componentRequiresDesktop(cfg.Runtime, rec.Kind),
			Hooks:            cfg.Hooks,
			Rollout:          extpipe.NormalizeRollout(rec.Rollout),
			Installed:        vis.Installed,
			Managed:          vis.Managed,
			InstalledAtMs:    vis.InstalledAtMs,
			MembersInstalled: memberCounts[componentKey(rec.Kind, rec.Name)],
		}
		if vis.Installed {
			perms := cfg.EffectivePermissions(rec)
			component.Permissions = &perms
			component.LocalSettings = cfg.LocalSettings
			component.Artifacts = cfg.Artifacts
			component.Endpoint = cfg.Endpoint
			component.Sidecar = cfg.Sidecar
			component.Capabilities = cfg.Capabilities
			component.Guest = cfg.Guest
			if rec.Kind == string(extreg.KindPreviewer) {
				component.Previewer = s.previewerCatalog(claims, rec, cfg)
			}
			if cfg.Protocol.Min != "" || cfg.Protocol.Max != "" {
				proto := cfg.Protocol
				component.Protocol = &proto
			}
		}
		out = append(out, component)
	}
	// Content components (experts, skill packs) the member may use, through
	// the same visibility function. They are listed apart from the
	// executable catalog the desktop supervises: nothing of theirs runs as a
	// process.
	var content []wire.ContentComponent
	if onlyKind == "" || isContentKind(onlyKind) {
		content, err = s.memberContent(r.Context(), claims, contentKindFilter(onlyKind))
		if err != nil {
			obs.ServerError(w, "load content components failed", err)
			http.Error(w, "load content components failed", http.StatusInternalServerError)
			return
		}
	}
	if content == nil {
		content = []wire.ContentComponent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"components": out, "contentComponents": content})
}

func isContentKind(kind string) bool {
	_, ok := contentpkg.Lookup(kind)
	return ok
}

func contentKindFilter(kind string) string {
	if isContentKind(kind) {
		return kind
	}
	return ""
}

func (s *Service) handleComponentInstall(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.ComponentInstalls == nil {
		obs.ServerError(w, "component installs unavailable", nil)
		http.Error(w, "component installs unavailable", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		http.Error(w, "component name required", http.StatusBadRequest)
		return
	}
	rec, ok := s.requestComponentRecord(w, r, name)
	if !ok {
		return
	}
	vis := extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForRecord(rec), 0)
	if !vis.Visible {
		http.Error(w, "component not found", http.StatusNotFound)
		return
	}
	if extpipe.NormalizeRollout(rec.Rollout) != extpipe.RolloutOptIn {
		http.Error(w, "component is not opt-in", http.StatusBadRequest)
		return
	}
	if err := s.ComponentInstalls.PutComponentInstall(r.Context(), claims.Subject, rec.Kind, rec.Name, s.nowTime().UnixMilli()); err != nil {
		obs.ServerError(w, "record install failed", err)
		http.Error(w, "record install failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleComponentUninstall(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.ComponentInstalls == nil {
		obs.ServerError(w, "component installs unavailable", nil)
		http.Error(w, "component installs unavailable", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		http.Error(w, "component name required", http.StatusBadRequest)
		return
	}
	rec, ok := s.requestComponentRecord(w, r, name)
	if !ok {
		return
	}
	if err := s.ComponentInstalls.DeleteComponentInstall(r.Context(), claims.Subject, rec.Kind, rec.Name); err != nil {
		obs.ServerError(w, "remove install failed", err)
		http.Error(w, "remove install failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
