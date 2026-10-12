package gateway

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
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
	Capabilities  []string                `json:"capabilities,omitempty"`
	Protocol      *protocol.ProtocolRange `json:"protocol,omitempty"`
	Rollout       string                  `json:"rollout"`
	Installed     bool                    `json:"installed"`
	Managed       bool                    `json:"managed"`
	InstalledAtMs int64                   `json:"installedAtMs,omitempty"`
	// MemberInstalls counts the members who opted in (the platform list's
	// adoption.memberInstalls).
	MemberInstalls int `json:"memberInstalls,omitempty"`
	// Guest is a sandbox's machine profile: the desktop prewarms it.
	Guest *pkgmeta.Guest `json:"guest,omitempty"`
	// Previewer is an installed previewer's match/toolbar metadata, its
	// pinned bundle, and the caller's browser frame URL (previewers.go).
	Previewer *previewerCatalogInfo `json:"previewer,omitempty"`
	// MCP is an MCP tool's tools, env and credentials source (mcp_tools.go).
	MCP *mcpCatalogInfo `json:"mcp,omitempty"`
}

func (s *Service) handleComponents(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	view, err := s.loadMemberComponents(r.Context(), claims)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	kinds := make([]string, 0, len(view.list))
	for _, rec := range view.list {
		kinds = append(kinds, rec.Kind)
	}
	memberCounts, err := s.componentInstallCounts(r.Context(), kinds)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	// ?kind= narrows the catalog to one kind (e.g. the web host reads only
	// previewers); absent lists every member-visible kind.
	onlyKind := strings.TrimSpace(r.URL.Query().Get("kind"))
	out := make([]componentInfo, 0, len(view.list))
	for _, rec := range view.list {
		if !componentKindVisibleInMemberCatalog(componentKindForName(rec)) || (onlyKind != "" && rec.Kind != onlyKind) {
			continue
		}
		vis := view.visibility(rec)
		if !vis.Visible {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			obs.ServerError(w, "load components failed", err)
			httpx.Error(w, "load components failed", http.StatusInternalServerError)
			return
		}
		component := componentInfo{
			Kind:            rec.Kind,
			Name:            rec.Name,
			Version:         rec.Version,
			Publisher:       rec.Publisher,
			Description:     cfg.Description,
			Runtime:         string(cfg.Runtime),
			Contract:        cfg.Contract,
			RequiresDesktop: componentRequiresDesktop(cfg.Runtime, rec.Kind),
			Hooks:           cfg.Hooks,
			Rollout:         extpipe.NormalizeRollout(rec.Rollout),
			Installed:       vis.Installed,
			Managed:         vis.Managed,
			InstalledAtMs:   vis.InstalledAtMs,
			MemberInstalls:  memberCounts[componentKey(rec.Kind, rec.Name)],
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
			if isMCP(rec) {
				component.MCP = s.mcpCatalog(r.Context(), claims, rec, cfg)
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
			httpx.Error(w, "load content components failed", http.StatusInternalServerError)
			return
		}
	}
	if content == nil {
		content = []wire.ContentComponent{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"components": out, "contentComponents": content})
}

func isContentKind(kind string) bool {
	_, ok := contentpkg.Lookup(kind)
	return ok
}

// contentKindFilter is the full content kind a catalog filter names, ""
// for every kind.
func contentKindFilter(kind string) string {
	if k, ok := contentpkg.Lookup(kind); ok {
		return k.Kind
	}
	return ""
}

func (s *Service) handleComponentInstall(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if s.ComponentInstalls == nil {
		obs.ServerError(w, "component installs unavailable", nil)
		httpx.Error(w, "component installs unavailable", http.StatusServiceUnavailable)
		return
	}
	rec, ok := s.requestComponentRecord(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	vis := extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForRecord(rec), 0)
	if !vis.Visible {
		httpx.Error(w, "component not found", http.StatusNotFound)
		return
	}
	if vis.Managed {
		httpx.Error(w, "component is not opt-in", http.StatusBadRequest)
		return
	}
	if err := s.ComponentInstalls.PutComponentInstall(r.Context(), claims.Subject, rec.Kind, rec.Name, s.nowTime().UnixMilli()); err != nil {
		obs.ServerError(w, "record install failed", err)
		httpx.Error(w, "record install failed", http.StatusInternalServerError)
		return
	}
	// An opt-in row is the member's in every organization, and their
	// desktop's stream is in its own session tenant, not a web acting
	// tenant: hint every desktop of the member.
	s.componentsChanged(r.Context(), "", claims.Subject)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleComponentUninstall(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if s.ComponentInstalls == nil {
		obs.ServerError(w, "component installs unavailable", nil)
		httpx.Error(w, "component installs unavailable", http.StatusServiceUnavailable)
		return
	}
	rec, ok := s.requestComponentRecord(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	if err := s.ComponentInstalls.DeleteComponentInstall(r.Context(), claims.Subject, rec.Kind, rec.Name); err != nil {
		obs.ServerError(w, "remove install failed", err)
		httpx.Error(w, "remove install failed", http.StatusInternalServerError)
		return
	}
	s.componentsChanged(r.Context(), "", claims.Subject)
	w.WriteHeader(http.StatusNoContent)
}
