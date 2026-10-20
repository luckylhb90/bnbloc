package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/componentsettings"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Component settings (docs/prd/2026-10-19-component-settings.md) on the
// gateway: the super admin sets a component's platform settings, an
// organization admin its organization settings (each scope's one form,
// checked against the declaration by pkgmeta.MergeSettings, sealed by
// internal/componentsettings, every save audited by key, never by value),
// the member catalog says which are set and which keep the component in
// needs setup, and a member's desktop reads the values of a component it
// runs (resolved). Member values never come here.

// componentSettingsLimit bounds a settings save body.
const componentSettingsLimit = 64 << 10

// componentSettings is the settings store (sealed with the deployment's
// secret store key; off without it).
func (s *Service) componentSettings() *componentsettings.Store {
	return &componentsettings.Store{Settings: s.PlatformSettings, Box: s.SecretBox, Now: func() int64 { return s.nowTime().UnixMilli() }}
}

// reachesMembers reports a component that runs on members' computers: its
// platform and organization values are sent to them (a plugin sidecar the
// desktop launches, a stdio MCP tool the bridge starts).
func reachesMembers(rec extpipe.Record, cfg extpipe.PackageConfig) bool {
	return rec.Kind == string(pkgmeta.KindPlugin) || (isMCP(rec) && cfg.Runtime == pkgmeta.RuntimeStdio)
}

// settingsRecord resolves {name} (and ?kind=) to an installed component and
// its config for a settings route; org scope also needs the component to be
// visible to the caller's organization. It answers a failure itself.
func (s *Service) settingsRecord(w http.ResponseWriter, r *http.Request, claims auth.Claims, scope string) (extpipe.Record, extpipe.PackageConfig, bool) {
	rec, ok := s.requestComponentRecord(w, r, r.PathValue("name"))
	if !ok {
		return extpipe.Record{}, extpipe.PackageConfig{}, false
	}
	if scope == pkgmeta.SettingScopeOrg && !orgSees(claims, rec) {
		httpx.Error(w, "component not found", http.StatusNotFound)
		return extpipe.Record{}, extpipe.PackageConfig{}, false
	}
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		obs.ServerError(w, "load component failed", err)
		httpx.Error(w, "load component failed", http.StatusInternalServerError)
		return extpipe.Record{}, extpipe.PackageConfig{}, false
	}
	return rec, cfg, true
}

// orgSees reports whether the caller's organization may see a component
// (listed to its members, before any opts in): whose organization settings
// its admins set, and whose settings page they may open.
func orgSees(claims auth.Claims, rec extpipe.Record) bool {
	return extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForRecord(rec), 0).Visible
}

// settingsTenant is the organization a scope's record belongs to.
func settingsTenant(scope string, claims auth.Claims) string {
	if scope == pkgmeta.SettingScopeOrg {
		return claims.Tenant
	}
	return ""
}

// settingsView is a scope's form: the component's settings of that scope
// with their state (a secret's value never), and the required settings of
// this scope and the ones above it that are not set.
func (s *Service) settingsView(ctx context.Context, scope string, claims auth.Claims, rec extpipe.Record, cfg extpipe.PackageConfig) (wire.ComponentSettingsView, error) {
	st := s.componentSettings()
	tenantID := settingsTenant(scope, claims)
	view := wire.ComponentSettingsView{
		Kind: rec.Kind, Name: rec.Name, Version: rec.Version, Description: cfg.Description, Scope: scope,
		Available: st.Available(), ReachesMembers: reachesMembers(rec, cfg), Asks: asksForSettings(rec),
		Fields: []wire.ComponentSettingField{}, Missing: []wire.ComponentSetting{},
		Page: s.settingsPageFrame(claims, scope, rec, cfg),
	}
	var stored componentsettings.Record
	if view.Available && len(pkgmeta.SettingsOfScope(cfg.Settings, scope)) > 0 {
		var err error
		if stored, _, err = st.Get(ctx, scope, tenantID, rec.Kind, rec.Name); err != nil {
			return wire.ComponentSettingsView{}, err
		}
	}
	view.UpdatedBy, view.UpdatedAtMs = stored.UpdatedBy, stored.UpdatedAtMs
	for _, setting := range pkgmeta.SettingsOfScope(cfg.Settings, scope) {
		v := strings.TrimSpace(stored.Values[setting.Key])
		f := wire.ComponentSettingField{ComponentSetting: setting, Set: v != ""}
		if setting.Type != pkgmeta.SettingSecret {
			f.Value = v
		}
		view.Fields = append(view.Fields, f)
	}
	known := []string{pkgmeta.SettingScopePlatform}
	if scope == pkgmeta.SettingScopeOrg {
		known = append(known, pkgmeta.SettingScopeOrg)
	}
	values, err := st.Values(ctx, claims.Tenant, rec.Kind, rec.Name, cfg.Settings, known...)
	if err != nil {
		return wire.ComponentSettingsView{}, err
	}
	unset := pkgmeta.UnsetSettings(cfg.Settings, values, known...)
	for _, setting := range cfg.Settings {
		if slices.Contains(unset, setting.Key) {
			view.Missing = append(view.Missing, setting)
		}
	}
	return view, nil
}

// handleComponentSettings answers GET /v1/platform/components/{name}/settings
// (platform scope) and GET /v1/tenant/components/{name}/settings (the caller's
// organization).
func (s *Service) handleComponentSettings(scope string) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
		rec, cfg, ok := s.settingsRecord(w, r, claims, scope)
		if !ok {
			return
		}
		view, err := s.settingsView(r.Context(), scope, claims, rec, cfg)
		if err != nil {
			obs.ServerError(w, "read component settings failed", err)
			httpx.Error(w, "read component settings failed", http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	}
}

// handlePutComponentSettings answers PUT of the same routes: the scope's
// values, checked against the declaration (pkgmeta.MergeSettings), sealed
// and audited; it answers the scope's view.
func (s *Service) handlePutComponentSettings(scope string) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
		rec, cfg, ok := s.settingsRecord(w, r, claims, scope)
		if !ok {
			return
		}
		var req wire.ComponentSettingsUpdate
		if !httpx.DecodeJSON(w, r, componentSettingsLimit, &req) {
			return
		}
		st := s.componentSettings()
		if !st.Available() {
			httpx.Error(w, componentsettings.ErrOff.Error(), http.StatusConflict)
			return
		}
		var changed []string
		var invalid error
		_, err := st.Update(r.Context(), scope, settingsTenant(scope, claims), rec.Kind, rec.Name, claims.Subject, func(current map[string]string) (map[string]string, error) {
			next, keys, err := pkgmeta.MergeSettings(cfg.Settings, scope, current, req.Values, req.Clear)
			invalid, changed = err, keys
			return next, err
		})
		if invalid != nil {
			// The refused setting's key goes with the reason: a form or a
			// component's settings page shows it at that field.
			body := httpx.ErrorBody{Error: invalid.Error()}
			var refusal *pkgmeta.SettingRefusal
			if errors.As(invalid, &refusal) {
				body.Field = refusal.Key
			}
			httpx.WriteJSON(w, http.StatusBadRequest, body)
			return
		}
		if err != nil {
			obs.ServerError(w, "save component settings failed", err)
			httpx.Error(w, "save component settings failed", http.StatusInternalServerError)
			return
		}
		s.audit(r.Context(), settingsTenant(scope, claims), store.ExtAuditRecord{
			Kind: rec.Kind, Name: rec.Name, Action: "settings-set", Actor: claims.Subject, Outcome: wire.AuditOutcomeAllow,
			Detail: "scope=" + scope + " keys=" + strings.Join(changed, ","),
		})
		if reachesMembers(rec, cfg) {
			// A desktop running the component relaunches it with the new
			// values (the catalog's settings state changed).
			s.componentsChanged(r.Context(), settingsTenant(scope, claims), "")
		}
		view, err := s.settingsView(r.Context(), scope, claims, rec, cfg)
		if err != nil {
			obs.ServerError(w, "read component settings failed", err)
			httpx.Error(w, "read component settings failed", http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, view)
	}
}

// handleResolvedComponentSettings answers GET
// /v1/components/{name}/settings/resolved: the platform and organization
// values (secrets included) of a component installed for the caller that
// runs on their computer, which their desktop hands it at launch. Never
// logged; reads are not audited.
func (s *Service) handleResolvedComponentSettings(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	rec, ok := s.requestComponentRecord(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	view, err := s.loadMemberComponents(r.Context(), claims)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		obs.ServerError(w, "load component failed", err)
		httpx.Error(w, "load component failed", http.StatusInternalServerError)
		return
	}
	if !view.visibility(rec).Installed || !reachesMembers(rec, cfg) {
		httpx.Error(w, "component not found", http.StatusNotFound)
		return
	}
	values, missing, err := s.componentSettings().Resolved(r.Context(), claims.Tenant, rec.Kind, rec.Name, cfg.Settings)
	if err != nil {
		obs.ServerError(w, "read component settings failed", err)
		httpx.Error(w, "read component settings failed", http.StatusInternalServerError)
		return
	}
	if missing == nil {
		missing = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, wire.ComponentSettingsResolved{Values: values, Missing: missing})
}

// catalogSettings fills a member catalog entry's settings state: which
// platform and organization settings hold a value for the member's
// organization, and the required ones that do not (needs setup). A store
// failure is logged and reads as every required one missing (fail closed:
// the component does not run on a guess).
func (s *Service) catalogSettings(ctx context.Context, claims auth.Claims, rec extpipe.Record, cfg extpipe.PackageConfig, out *wire.MemberComponent) {
	out.Settings, out.SettingsPage = cfg.Settings, cfg.SettingsPage
	scopes := []string{pkgmeta.SettingScopePlatform, pkgmeta.SettingScopeOrg}
	if len(pkgmeta.SettingsOfScope(cfg.Settings, scopes...)) == 0 {
		return
	}
	values, missing, err := s.componentSettings().Resolved(ctx, claims.Tenant, rec.Kind, rec.Name, cfg.Settings)
	if err != nil {
		slog.Warn("bnbloc-gateway: read component settings failed",
			"event", "bnbloc.gateway.component_settings_unreadable", "kind", rec.Kind, "component", rec.Name, "error", err)
		values, missing = nil, pkgmeta.UnsetSettings(cfg.Settings, nil, scopes...)
	}
	for _, setting := range cfg.Settings {
		if values[setting.Key] != "" {
			out.SettingsSet = append(out.SettingsSet, setting.Key)
		}
	}
	out.SettingsMissing = missing
}

// afterSettingsUninstall deletes every scope's settings of an uninstalled
// component: a later install under the same name starts unset.
func (s *Service) afterSettingsUninstall(ctx context.Context, rec extpipe.Record) {
	if err := s.componentSettings().DeleteComponent(ctx, rec.Kind, rec.Name); err != nil && !errors.Is(err, componentsettings.ErrOff) {
		slog.Warn("bnbloc-gateway: delete component settings failed", "kind", rec.Kind, "component", rec.Name, "error", err)
	}
}
