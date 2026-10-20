package gateway

import (
	"errors"
	"net/http"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/componentcred"
	"github.com/luckylhb90/bnbloc/internal/componentsettings"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Endpoint components ask for their settings
// (docs/prd/2026-10-19-component-settings.md §2.3, slice 3): a notifier or
// an auth provider runs beside the gateway, so nothing is delivered to it;
// it asks GET /v1/component-settings, signed with its component credential,
// and is answered its platform and organization values.

// asksForSettings reports an installed endpoint component that asks for its
// settings itself.
func asksForSettings(rec extpipe.Record) bool { return componentcred.Holds(rec.Kind) }

// handleEndpointComponentSettings answers GET /v1/component-settings to the
// endpoint component that signed it (X-Bnbloc-Component "<kind>/<name>",
// X-Bnbloc-Signature over "GET /v1/component-settings"): its stored
// platform values, the organization values of the organizations the
// install serves, the required ones not set, and the answer's version.
// Secrets are included; the answer is never logged and reads are not
// audited (as the resolved values a member's desktop reads).
func (s *Service) handleEndpointComponentSettings(w http.ResponseWriter, r *http.Request) {
	kind, name := componentCaller(r)
	rec, ok := s.verifyComponentCall(w, r, kind, name, componentCallSigned(r))
	if !ok {
		return
	}
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		obs.ServerError(w, "load component failed", err)
		httpx.Error(w, "load component failed", http.StatusInternalServerError)
		return
	}
	answer, err := s.componentSettings().Endpoint(r.Context(), rec.Kind, rec.Name, cfg.Settings, func(tenantID string) bool {
		return extpipe.TenantDistribution(rec, tenantID).Distributed
	})
	switch {
	case errors.Is(err, componentsettings.ErrOff):
		httpx.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		obs.ServerError(w, "read component settings failed", err)
		httpx.Error(w, "read component settings failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, answer)
}

// handleOrgSettingsComponents answers GET /v1/tenant/component-settings:
// every installed component visible to the caller's organization that
// declares organization settings, of any kind (an endpoint component beside
// the gateway is never in a member's catalog), with what it still needs.
func (s *Service) handleOrgSettingsComponents(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	recs, err := s.corePipeline().List(r.Context(), "")
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	out := wire.OrgSettingsComponentsReply{Components: []wire.OrgSettingsComponent{}}
	for _, rec := range recs {
		if !orgSees(claims, rec) {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil || len(pkgmeta.SettingsOfScope(cfg.Settings, pkgmeta.SettingScopeOrg)) == 0 {
			continue
		}
		var entry wire.MemberComponent
		s.catalogSettings(r.Context(), claims, rec, cfg, &entry)
		missing := entry.SettingsMissing
		if missing == nil {
			missing = []string{}
		}
		out.Components = append(out.Components, wire.OrgSettingsComponent{
			Kind: rec.Kind, Name: rec.Name, Version: rec.Version, Description: cfg.Description,
			Settings: cfg.Settings, SettingsPage: cfg.SettingsPage, SettingsMissing: missing,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
