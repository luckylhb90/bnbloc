package shareagenthttp

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Data retention policy (docs/prd/2026-10-10-enterprise-scale.md §3).
// shareagent owns the tenant registry and the platform policies, so it
// serves both layers; the retention worker (realtime) reads them.
//
//   - GET/PUT /v1/platform/retention: the platform default (super-admin;
//     manage_platform at the gateway).
//   - GET/PUT /v1/tenant/retention: the ctx tenant's override (org admin;
//     manage_org at the gateway, which binds the tenant).

// retentionReady answers 501 when the stores are not configured.
func (s *Service) retentionReady(w http.ResponseWriter) bool {
	if s.Settings == nil || s.Tenants == nil {
		obs.ServerError(w, "retention policy not configured", nil)
		writeErr(w, http.StatusNotImplemented, "retention policy not configured")
		return false
	}
	return true
}

// platformRetentionView reads the default and the worker status.
func (s *Service) platformRetentionView(w http.ResponseWriter, r *http.Request) (wire.PlatformRetentionView, bool) {
	def, err := store.LoadPlatformRetention(r.Context(), s.Settings)
	if err != nil {
		obs.ServerError(w, "read retention default failed", err)
		writeErr(w, http.StatusInternalServerError, "read retention default failed")
		return wire.PlatformRetentionView{}, false
	}
	status, err := store.LoadRetentionStatus(r.Context(), s.Settings)
	if err != nil {
		obs.ServerError(w, "read retention status failed", err)
		writeErr(w, http.StatusInternalServerError, "read retention status failed")
		return wire.PlatformRetentionView{}, false
	}
	return wire.PlatformRetentionView{Default: def, Limits: wire.CurrentRetentionLimits(), Status: status}, true
}

func (s *Service) handleGetPlatformRetention(w http.ResponseWriter, r *http.Request) {
	if !s.retentionReady(w) {
		return
	}
	if v, ok := s.platformRetentionView(w, r); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// handlePutPlatformRetention replaces the platform default. Each kind is
// null or 0 (keep forever) or a day count within the limits.
func (s *Service) handlePutPlatformRetention(w http.ResponseWriter, r *http.Request) {
	if !s.retentionReady(w) {
		return
	}
	var body wire.RetentionPolicy
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := body.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	actor := callerSubject(r)
	if err := store.SavePlatformRetention(r.Context(), s.Settings, body, actor, s.nowMs()); err != nil {
		obs.ServerError(w, "save retention default failed", err)
		writeErr(w, http.StatusInternalServerError, "save retention default failed")
		return
	}
	s.audit(r.Context(), store.ExtAuditRecord{Kind: wire.AuditKindRetention, Name: "platform", Action: "policy", Actor: actor, Detail: retentionDetail(body.AsDefault(), "forever")})
	if v, ok := s.platformRetentionView(w, r); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// tenantRetentionView builds the ctx tenant's view.
func (s *Service) tenantRetentionView(w http.ResponseWriter, r *http.Request, t store.Tenant) (wire.TenantRetentionView, bool) {
	def, err := store.LoadPlatformRetention(r.Context(), s.Settings)
	if err != nil {
		obs.ServerError(w, "read retention default failed", err)
		writeErr(w, http.StatusInternalServerError, "read retention default failed")
		return wire.TenantRetentionView{}, false
	}
	return wire.TenantRetentionView{
		TenantID: t.TenantID, Override: t.Retention, Default: def,
		Effective: wire.EffectiveRetention(def, t.Retention), Limits: wire.CurrentRetentionLimits(),
	}, true
}

func (s *Service) handleGetTenantRetention(w http.ResponseWriter, r *http.Request) {
	if !s.retentionReady(w) {
		return
	}
	t, err := s.Tenants.GetTenant(r.Context(), tenant.FromContext(r.Context()))
	if err != nil {
		writeErr(w, http.StatusNotFound, "tenant not found")
		return
	}
	if v, ok := s.tenantRetentionView(w, r, t); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// handlePutTenantRetention replaces the ctx tenant's whole override: a null
// kind uses the platform default, 0 keeps forever, N deletes after N days.
func (s *Service) handlePutTenantRetention(w http.ResponseWriter, r *http.Request) {
	if !s.retentionReady(w) {
		return
	}
	var body wire.RetentionPolicy
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := body.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tid := tenant.FromContext(r.Context())
	t, err := s.Tenants.SetTenantRetention(r.Context(), tid, body)
	if err == store.ErrTenantNotFound {
		writeErr(w, http.StatusNotFound, "tenant not found")
		return
	}
	if err != nil {
		obs.ServerError(w, "save tenant retention failed", err)
		writeErr(w, http.StatusInternalServerError, "save tenant retention failed")
		return
	}
	s.audit(r.Context(), store.ExtAuditRecord{Kind: wire.AuditKindRetention, Name: tid, Action: "policy", Actor: callerSubject(r), TenantID: tid, Detail: retentionDetail(body, "default")})
	if v, ok := s.tenantRetentionView(w, r, t); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// retentionDetail renders a policy for the audit log
// ("messages=90 artifactVersions=default audit=forever agentJobs=default");
// unset says what null means at the policy's layer.
func retentionDetail(p wire.RetentionPolicy, unset string) string {
	parts := make([]string, 0, len(wire.RetentionKinds))
	for _, k := range wire.RetentionKinds {
		v := unset
		if d := p.Days(k); d != nil {
			v = "forever"
			if *d > 0 {
				v = strconv.Itoa(*d)
			}
		}
		parts = append(parts, string(k)+"="+v)
	}
	return strings.Join(parts, " ")
}
