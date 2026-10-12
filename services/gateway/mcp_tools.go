package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/mcptool"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MCP tool components (docs/prd/2026-10-12-experts-and-teams.md §6) on the
// gateway: the save-time check of content references, the per-job
// resolution the bridge builds a session's MCP servers from, and the
// organization connectors org admins configure. Install, rollout and
// tenants are the shared component routes; nothing of a tool runs here.

func isMCP(rec extpipe.Record) bool { return rec.Kind == string(extreg.KindMCP) }

// mcpConnectors is the organization connector store (sealed with the
// deployment's secret store key; off without it).
func (s *Service) mcpConnectors() *mcptool.Connectors {
	return &mcptool.Connectors{Settings: s.PlatformSettings, Box: s.SecretBox, Now: func() int64 { return s.nowTime().UnixMilli() }}
}

// mcpCandidates reads the installed MCP components as one member may use
// them, through the one visibility function.
func (s *Service) mcpCandidates(ctx context.Context, claims auth.Claims) (map[string]mcptool.Candidate, error) {
	view, err := s.loadMemberComponents(ctx, claims)
	if err != nil {
		return nil, err
	}
	out := map[string]mcptool.Candidate{}
	for _, rec := range view.records {
		if !isMCP(rec) {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			return nil, err
		}
		out[rec.Name] = mcptool.Candidate{Record: rec, Config: cfg, Visibility: view.visibility(rec)}
	}
	return out, nil
}

// saveContentOptions are the validation inputs of a content save by
// claims: requires.mcp must name an MCP component the member may use. It
// answers a store failure itself.
func (s *Service) saveContentOptions(w http.ResponseWriter, r *http.Request, claims auth.Claims) (contentpkg.Options, bool) {
	cands, err := s.mcpCandidates(r.Context(), claims)
	if err != nil {
		obs.ServerError(w, "load MCP tools failed", err)
		httpx.Error(w, "load MCP tools failed", http.StatusInternalServerError)
		return contentpkg.Options{}, false
	}
	return contentpkg.Options{MCPTool: func(name string) (contentpkg.MCPToolInfo, bool) {
		c, ok := cands[name]
		if !ok || !c.Visibility.Visible || c.Config.MCP == nil {
			return contentpkg.MCPToolInfo{}, false
		}
		return contentpkg.MCPToolInfo{Transport: mcptool.Transport(c.Config), Host: mcptool.Host(c.Config), Env: mcptool.EnvNames(c.Config)}, true
	}}, true
}

// resolveEffectiveMCP adds the MCP tools the job's content requires: each
// must be an installed MCP component the requester may use; an org
// credential tool brings the job's organization connector values. Unmet
// requirements join eff.Missing (the turn still runs).
func (s *Service) resolveEffectiveMCP(ctx context.Context, job store.AgentJob, eff *wire.ContentEffective) error {
	refs := append([]wire.ContentRef(nil), eff.SkillPacks...)
	for _, r := range []*wire.ContentRef{eff.Expert, eff.RoomExpert} {
		if r != nil {
			refs = append(refs, *r)
		}
	}
	needs := false
	for _, r := range refs {
		needs = needs || len(r.MCP) > 0
	}
	if !needs {
		return nil
	}
	cands, err := s.mcpCandidates(ctx, auth.Claims{Subject: job.RequesterUserID, Tenant: job.TenantID})
	if err != nil {
		return err
	}
	connectors := s.mcpConnectors()
	tools, missing, err := mcptool.Resolve(refs,
		func(name string) (mcptool.Candidate, bool) { c, ok := cands[name]; return c, ok },
		func(name string) (map[string]string, bool, error) {
			c, ok, err := connectors.Get(ctx, job.TenantID, name)
			return c.Values, ok, err
		})
	if err != nil {
		return err
	}
	eff.MCP = tools
	eff.Missing = append(eff.Missing, missing...)
	return nil
}

// afterMCPUninstall deletes every organization's connector of an
// uninstalled MCP component: a later install under the same name starts
// unconfigured.
func (s *Service) afterMCPUninstall(ctx context.Context, rec extpipe.Record) {
	if !isMCP(rec) {
		return
	}
	if err := s.mcpConnectors().DeleteComponent(ctx, rec.Name); err != nil {
		slog.Warn("bnbloc-gateway: delete MCP connectors failed", "component", rec.Name, "error", err)
	}
}

// mcpCatalogInfo is an installed MCP component in the member catalog: its
// tools, env and credentials source, and for org credentials whether the
// member's organization configured its connector.
type mcpCatalogInfo struct {
	*pkgmeta.MCP
	Transport string `json:"transport"`
	// OrgConfigured is set for org credentials.
	OrgConfigured *bool `json:"orgConfigured,omitempty"`
}

func (s *Service) mcpCatalog(ctx context.Context, claims auth.Claims, rec extpipe.Record, cfg extpipe.PackageConfig) *mcpCatalogInfo {
	if cfg.MCP == nil {
		return nil
	}
	out := &mcpCatalogInfo{MCP: cfg.MCP, Transport: mcptool.Transport(cfg)}
	if cfg.MCP.Credentials == pkgmeta.MCPCredentialsOrg {
		configured := false
		if c, ok, err := s.mcpConnectors().Get(ctx, claims.Tenant, rec.Name); err == nil && ok {
			configured = mcptool.Configured(cfg.MCP.Env, c.Values)
		} else if err != nil && !errors.Is(err, mcptool.ErrConnectorsOff) {
			slog.Warn("bnbloc-gateway: read MCP connector failed", "component", rec.Name, "error", err)
		}
		out.OrgConfigured = &configured
	}
	return out
}

// --- organization connectors (org admins) ---

// orgMCPRecord resolves {name} to an org-credential MCP component visible
// to the caller's organization.
func (s *Service) orgMCPRecord(w http.ResponseWriter, r *http.Request, claims auth.Claims) (extpipe.Record, extpipe.PackageConfig, bool) {
	name := strings.TrimSpace(r.PathValue("name"))
	rec, ok, err := s.findComponentRecord(r.Context(), extreg.KindMCP, name)
	if err != nil {
		obs.ServerError(w, "load MCP tools failed", err)
		httpx.Error(w, "load MCP tools failed", http.StatusInternalServerError)
		return extpipe.Record{}, extpipe.PackageConfig{}, false
	}
	if !ok || !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForRecord(rec), 0).Visible {
		httpx.Error(w, "MCP tool not found", http.StatusNotFound)
		return extpipe.Record{}, extpipe.PackageConfig{}, false
	}
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		obs.ServerError(w, "load MCP tools failed", err)
		httpx.Error(w, "load MCP tools failed", http.StatusInternalServerError)
		return extpipe.Record{}, extpipe.PackageConfig{}, false
	}
	if cfg.MCP == nil || cfg.MCP.Credentials != pkgmeta.MCPCredentialsOrg {
		httpx.Error(w, "this MCP tool takes no organization connector", http.StatusBadRequest)
		return extpipe.Record{}, extpipe.PackageConfig{}, false
	}
	return rec, cfg, true
}

// handleListMCPConnectors answers GET /v1/tenant/mcp-connectors: the
// org-credential MCP tools visible to the organization with their
// connector's state (secret values never returned).
func (s *Service) handleListMCPConnectors(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	recs, err := s.corePipeline().List(r.Context(), extreg.KindMCP)
	if err != nil {
		obs.ServerError(w, "load MCP tools failed", err)
		httpx.Error(w, "load MCP tools failed", http.StatusInternalServerError)
		return
	}
	connectors := s.mcpConnectors()
	reply := wire.MCPConnectorsReply{Available: connectors.Available(), Connectors: []wire.MCPConnector{}}
	for _, rec := range recs {
		if !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForRecord(rec), 0).Visible {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			obs.ServerError(w, "load MCP tools failed", err)
			httpx.Error(w, "load MCP tools failed", http.StatusInternalServerError)
			return
		}
		if cfg.MCP == nil || cfg.MCP.Credentials != pkgmeta.MCPCredentialsOrg {
			continue
		}
		var stored mcptool.Connector
		if reply.Available {
			c, _, err := connectors.Get(r.Context(), claims.Tenant, rec.Name)
			if err != nil {
				obs.ServerError(w, "read MCP connector failed", err)
				httpx.Error(w, "read MCP connector failed", http.StatusInternalServerError)
				return
			}
			stored = c
		}
		view := wire.MCPConnector{
			Name: rec.Name, Version: rec.Version, Description: cfg.Description, Transport: mcptool.Transport(cfg),
			Configured: mcptool.Configured(cfg.MCP.Env, stored.Values), UpdatedBy: stored.UpdatedBy, UpdatedAtMs: stored.UpdatedAtMs,
			Fields: []wire.MCPConnectorField{},
		}
		for _, e := range cfg.MCP.Env {
			v := stored.Values[e.Name]
			f := wire.MCPConnectorField{Name: e.Name, Description: e.Description, Secret: e.Secret, Required: e.Required, Set: v != ""}
			if !e.Secret {
				f.Value = v
			}
			view.Fields = append(view.Fields, f)
		}
		reply.Connectors = append(reply.Connectors, view)
	}
	sort.Slice(reply.Connectors, func(i, j int) bool { return reply.Connectors[i].Name < reply.Connectors[j].Name })
	httpx.WriteJSON(w, http.StatusOK, reply)
}

// handlePutMCPConnector answers PUT /v1/tenant/mcp-connectors/{name}:
// store the organization's values (an empty secret keeps the stored one).
func (s *Service) handlePutMCPConnector(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	rec, cfg, ok := s.orgMCPRecord(w, r, claims)
	if !ok {
		return
	}
	connectors := s.mcpConnectors()
	if !connectors.Available() {
		httpx.Error(w, mcptool.ErrConnectorsOff.Error(), http.StatusConflict)
		return
	}
	var req wire.MCPConnectorUpdate
	if !httpx.DecodeJSON(w, r, 64<<10, &req) {
		return
	}
	current, _, err := connectors.Get(r.Context(), claims.Tenant, rec.Name)
	if err != nil {
		obs.ServerError(w, "read MCP connector failed", err)
		httpx.Error(w, "read MCP connector failed", http.StatusInternalServerError)
		return
	}
	next, err := mcptool.MergeValues(cfg.MCP.Env, current.Values, req.Values)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := connectors.Put(r.Context(), claims.Tenant, rec.Name, next, claims.Subject); err != nil {
		obs.ServerError(w, "save MCP connector failed", err)
		httpx.Error(w, "save MCP connector failed", http.StatusInternalServerError)
		return
	}
	s.auditMCPConnector(r.Context(), rec, claims, "connector-set")
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteMCPConnector answers DELETE /v1/tenant/mcp-connectors/{name}.
func (s *Service) handleDeleteMCPConnector(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	rec, _, ok := s.orgMCPRecord(w, r, claims)
	if !ok {
		return
	}
	if err := s.mcpConnectors().Delete(r.Context(), claims.Tenant, rec.Name); err != nil {
		obs.ServerError(w, "delete MCP connector failed", err)
		httpx.Error(w, "delete MCP connector failed", http.StatusInternalServerError)
		return
	}
	s.auditMCPConnector(r.Context(), rec, claims, "connector-clear")
	w.WriteHeader(http.StatusNoContent)
}

// auditMCPConnector records a connector change (names only, never values).
func (s *Service) auditMCPConnector(ctx context.Context, rec extpipe.Record, claims auth.Claims, action string) {
	s.audit(ctx, claims.Tenant, store.ExtAuditRecord{
		Kind: rec.Kind, Name: rec.Name, Action: action, Actor: claims.Subject, Outcome: "allow",
	})
}
