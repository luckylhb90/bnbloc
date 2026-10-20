package gateway

import (
	"context"
	"net/http"
	"sort"

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
// gateway: the save-time check of content references and the per-job
// resolution the bridge builds a session's MCP servers from. Install,
// rollout, tenants and settings are the shared component routes; nothing
// of a tool runs here.

func isMCP(rec extpipe.Record) bool { return rec.Kind == string(extreg.KindMCP) }

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
	return contentpkg.Options{Contracts: true, MCPTool: func(name string) (contentpkg.MCPToolInfo, bool) {
		c, ok := cands[name]
		if !ok || !c.Visibility.Visible || c.Config.MCP == nil {
			return contentpkg.MCPToolInfo{}, false
		}
		return contentpkg.MCPToolInfo{Transport: mcptool.Transport(c.Config), Host: mcptool.Host(c.Config), Settings: mcptool.SettingKeys(c.Config)}, true
	}}, true
}

// resolveEffectiveMCP adds the job's MCP tools: the ones its content
// requires and every one its requester installed
// (docs/prd/2026-10-17-browser-component.md §3.6), each an installed MCP
// component the requester may use, with its platform and organization
// setting values for the job's organization (one that needs setup is not
// offered). Unmet requirements join eff.Missing (the turn still runs).
func (s *Service) resolveEffectiveMCP(ctx context.Context, job store.AgentJob, eff *wire.ContentEffective) error {
	refs := append([]wire.ContentRef(nil), eff.SkillPacks...)
	for _, r := range []*wire.ContentRef{eff.Expert, eff.RoomExpert} {
		if r != nil {
			refs = append(refs, *r)
		}
	}
	cands, err := s.mcpCandidates(ctx, auth.Claims{Subject: job.RequesterUserID, Tenant: job.TenantID})
	if err != nil {
		return err
	}
	var installed []string
	for name, c := range cands {
		if c.Visibility.Installed {
			installed = append(installed, name)
		}
	}
	sort.Strings(installed)
	settings := s.componentSettings()
	tools, missing, err := mcptool.Resolve(refs, installed,
		func(name string) (mcptool.Candidate, bool) { c, ok := cands[name]; return c, ok },
		func(name string, decl []pkgmeta.Setting) (map[string]string, []string, error) {
			return settings.Resolved(ctx, job.TenantID, string(extreg.KindMCP), name, decl)
		})
	if err != nil {
		return err
	}
	eff.MCP = tools
	eff.Missing = append(eff.Missing, missing...)
	return nil
}

func mcpCatalog(cfg extpipe.PackageConfig) *wire.MemberComponentMCP {
	if cfg.MCP == nil {
		return nil
	}
	return &wire.MemberComponentMCP{ComponentMCP: *cfg.MCP, Transport: mcptool.Transport(cfg)}
}
