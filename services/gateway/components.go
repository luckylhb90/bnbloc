package gateway

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handleComponents is the member catalog: the executable components the
// member may use and their Library's content components. With a ?job=&room=
// grant (contentGrant: the bridge running the job acts as its requester) it
// is the requester's Library as an agent reads it (the room tools'
// list_components, the bridge's .room/library.md): content components and
// MCP tools only, never the executable catalog a desktop supervises.
func (s *Service) handleComponents(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	claims := actor.Claims
	library := actor.Job != nil
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
	out := make([]wire.MemberComponent, 0, len(view.list))
	for _, rec := range view.list {
		vis, listed := view.listed(rec)
		if !listed || (onlyKind != "" && rec.Kind != onlyKind) || (library && !isMCP(rec)) {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			obs.ServerError(w, "load components failed", err)
			httpx.Error(w, "load components failed", http.StatusInternalServerError)
			return
		}
		component := wire.MemberComponent{
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
			PublishedAtMs:   rec.InstalledAtMs,
			MemberInstalls:  memberCounts[componentKey(rec.Kind, rec.Name)],
		}
		s.catalogSettings(r.Context(), claims, rec, cfg, &component)
		if vis.Installed {
			perms := cfg.EffectivePermissions(rec)
			component.Permissions = &perms
			component.Artifacts = cfg.Artifacts
			component.Endpoint = cfg.Endpoint
			component.Sidecar = cfg.Sidecar
			component.Capabilities = cfg.Capabilities
			component.Guest = cfg.Guest
			if rec.Kind == string(extreg.KindPreviewer) {
				component.Previewer = s.previewerCatalog(claims, rec, cfg)
			}
			if isMCP(rec) {
				component.MCP = mcpCatalog(cfg)
			}
			if cfg.Protocol.Min != "" || cfg.Protocol.Max != "" {
				proto := componentProtocol(cfg.Protocol)
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
	httpx.WriteJSON(w, http.StatusOK, wire.ComponentsReply{Components: out, ContentComponents: content})
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
