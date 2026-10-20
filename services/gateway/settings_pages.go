package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A component's own settings page (docs/architecture/settings-pages.md) in
// the gateway: its bundle is probed at install like a previewer's, a
// scope's settings view hands the browser a frame for it, and the frame
// route serves its files exactly as a previewer's (serveFrameFile: the same
// verified archive, cache and policy). Whoever may set a scope may open its
// page there (the scope's permission, and for an organization the
// component visible to it); what the page reads and saves goes through the
// web host and the settings routes, never this route. A member's own scope
// is edited on their desktop, which serves the page itself.

// settingsPagePerm is who may open a scope's page: who may set the scope.
var settingsPagePerm = map[string]rbac.Permission{
	pkgmeta.SettingScopePlatform: rbac.PermManagePlatform,
	pkgmeta.SettingScopeOrg:      rbac.PermManageOrg,
}

func settingsPageTicketScope(scope, kind, name, version string) string {
	return scope + ":" + kind + "/" + name + "@" + version
}

func settingsPageFrameBase(kind, name, version string) string {
	return "/v1/settings-pages/" + url.PathEscape(kind) + "/" + url.PathEscape(name) + "/" + url.PathEscape(version) + "/"
}

// settingsPageFrame is the page of a component that declares one, for the
// scope a settings view shows: a frame ticket bound to the caller, the scope
// and this exact version (an upgrade retires older frames), and the bundle
// pin the desktop serves the page by.
func (s *Service) settingsPageFrame(claims auth.Claims, scope string, rec extpipe.Record, cfg extpipe.PackageConfig) *wire.ComponentSettingsPageFrame {
	page := cfg.SettingsPage
	if page == nil {
		return nil
	}
	expires := s.nowTime().Add(previewerFrameTTL)
	ticket := s.Signer.Issue(auth.Claims{
		Subject:       claims.Subject,
		Tenant:        claims.Tenant,
		Role:          claims.Role,
		ExpiresAtUnix: expires.Unix(),
		Kind:          auth.KindSettingsPageFrame,
		ID:            settingsPageTicketScope(scope, rec.Kind, rec.Name, rec.Version),
	})
	return &wire.ComponentSettingsPageFrame{
		Entry:            page.Entry,
		BundleSHA256:     page.Bundle.SHA256,
		FrameURL:         settingsPageFrameBase(rec.Kind, rec.Name, rec.Version) + url.PathEscape(ticket) + "/",
		FrameExpiresAtMs: expires.UnixMilli(),
	}
}

// handleSettingsPageFile serves one file of a component's settings page
// bundle to a browser frame: GET /v1/settings-pages/{kind}/{name}/{version}/
// {ticket}/{path...}. The ticket must be valid for this component, version
// and a scope, whose rule is re-checked on every request. Anything else is
// a 404, indistinguishable from a missing file.
func (s *Service) handleSettingsPageFile(w http.ResponseWriter, r *http.Request) {
	kind, name, version, file := r.PathValue("kind"), r.PathValue("name"), r.PathValue("version"), r.PathValue("path")
	if !core.ValidBundlePath(file) {
		http.NotFound(w, r)
		return
	}
	claims, err := s.Signer.VerifyKind(r.PathValue("ticket"), auth.KindSettingsPageFrame)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	scope, _, _ := strings.Cut(claims.ID, ":")
	if claims.ID != settingsPageTicketScope(scope, kind, name, version) {
		http.NotFound(w, r)
		return
	}
	page, ok, err := s.openableSettingsPage(r.Context(), claims, scope, kind, name, version)
	if err != nil {
		obs.ServerError(w, "load component failed", err)
		httpx.Error(w, "load component failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveFrameFile(w, r, settingsPageFrameBase(kind, name, version), page.Bundle, file, claims.ExpiresAtUnix)
}

// openableSettingsPage resolves the settings page of an installed component
// at exactly version, when claims may still set scope: the scope's
// permission, and for an organization the component visible to it (the
// rule of the settings routes, settingsRecord).
func (s *Service) openableSettingsPage(ctx context.Context, claims auth.Claims, scope, kind, name, version string) (*pkgmeta.SettingsPage, bool, error) {
	perm, known := settingsPagePerm[scope]
	if !known || !rbac.Can(claims.Role, perm) {
		return nil, false, nil
	}
	view, err := s.loadMemberComponents(ctx, claims)
	if err != nil {
		return nil, false, err
	}
	rec, ok := view.record(extreg.Kind(kind), name)
	if !ok || rec.Version != version {
		return nil, false, nil
	}
	if scope == pkgmeta.SettingScopeOrg && !orgSees(claims, rec) {
		return nil, false, nil
	}
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		return nil, false, err
	}
	return cfg.SettingsPage, cfg.SettingsPage != nil, nil
}

// probeSettingsPage proves a package's settings page installable, like a
// previewer's bundle (probeFrameBundle). The install's artifact probe runs
// it for every kind.
func (s *Service) probeSettingsPage(ctx context.Context, pkg pkgmeta.Package) error {
	if pkg.SettingsPage == nil {
		return nil
	}
	return s.probeFrameBundle(ctx, pkgmeta.SettingsPageBundle, pkg.SettingsPage.Bundle, pkg.SettingsPage.Entry)
}
