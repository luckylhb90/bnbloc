package shareagenthttp

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// platformTenantView is one tenant row for the platform (super-admin) console.
// It reuses the org settings projection plus the org-level quota so the
// cross-tenant overview can show ceilings without a second call per tenant.
type platformTenantView struct {
	TenantID      string  `json:"tenantId"`
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	SSOIssuer     string  `json:"ssoIssuer"`
	RunQuota      int     `json:"runQuota"`
	CostAllowance float64 `json:"costAllowance"`
	CreatedAtMs   int64   `json:"createdAtMs"`
}

// handlePlatformTenants lists every tenant. It is cross-tenant by design and is
// gated at the gateway by PermManagePlatform (super_admin only); the ctx tenant
// is irrelevant here. A non-super-admin never reaches this handler.
func (s *Service) handlePlatformTenants(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	tenants := s.Tenants.ListTenants(r.Context())
	views := make([]platformTenantView, 0, len(tenants))
	for _, t := range tenants {
		views = append(views, platformTenantView{
			TenantID:      t.TenantID,
			Name:          t.Name,
			Status:        string(t.Status),
			SSOIssuer:     t.SSOIssuer,
			RunQuota:      t.RunQuota,
			CostAllowance: t.CostAllowance,
			CreatedAtMs:   t.CreatedAtMs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": views})
}

type createTenantBody struct {
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	SSOIssuer string `json:"ssoIssuer"`
}

// handlePlatformCreateTenant creates a new tenant. Gated at the gateway by
// PermManagePlatform (super_admin only). The tenant id must be a non-empty
// slug; a duplicate id is a conflict.
func (s *Service) handlePlatformCreateTenant(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	var body createTenantBody
	if !decodeJSON(w, r, &body) {
		return
	}
	id := tenantSlug(body.TenantID)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "tenant id must be a non-empty slug (a-z 0-9 - _)")
		return
	}
	if _, err := s.Tenants.GetTenant(r.Context(), id); err == nil {
		writeErr(w, http.StatusConflict, "tenant already exists")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = id
	}
	t := store.Tenant{
		TenantID:    id,
		Name:        name,
		Status:      store.TenantActive,
		SSOIssuer:   strings.TrimSpace(body.SSOIssuer),
		CreatedAtMs: s.Now(),
	}
	if err := s.Tenants.PutTenant(r.Context(), t); err != nil {
		obs.ServerError(w, "create tenant failed", err)
		writeErr(w, http.StatusInternalServerError, "create tenant failed")
		return
	}
	s.auditTenant(r, id, "create", "name="+name)
	writeJSON(w, http.StatusCreated, platformTenantView{
		TenantID: t.TenantID, Name: t.Name, Status: string(t.Status),
		SSOIssuer: t.SSOIssuer, CreatedAtMs: t.CreatedAtMs,
	})
}

type statusBody struct {
	Status string `json:"status"`
}

// handlePlatformSetTenantStatus activates or suspends a tenant. A suspended
// tenant's members are refused at login (super-admins remain exempt). The
// default tenant cannot be suspended — it is the fallback scope for every
// legacy/dev session, so suspending it would lock everyone out.
func (s *Service) handlePlatformSetTenantStatus(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	id := r.PathValue("tenantId")
	var body statusBody
	if !decodeJSON(w, r, &body) {
		return
	}
	status := store.TenantStatus(strings.TrimSpace(body.Status))
	if status != store.TenantActive && status != store.TenantSuspended {
		writeErr(w, http.StatusBadRequest, "status must be active or suspended")
		return
	}
	if id == tenant.Default && status == store.TenantSuspended {
		writeErr(w, http.StatusConflict, "the default tenant cannot be suspended")
		return
	}
	t, err := s.Tenants.GetTenant(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "tenant not found")
		return
	}
	t.Status = status
	if err := s.Tenants.PutTenant(r.Context(), t); err != nil {
		obs.ServerError(w, "update status failed", err)
		writeErr(w, http.StatusInternalServerError, "update status failed")
		return
	}
	s.auditTenant(r, id, "status", "status="+string(status))
	if status == store.TenantSuspended {
		s.revokeSessionsFor(r.Context(), suspendedTenantRevokeFilter(id), "tenant "+id+" suspended")
	}
	writeJSON(w, http.StatusOK, platformTenantView{
		TenantID: t.TenantID, Name: t.Name, Status: string(t.Status),
		SSOIssuer: t.SSOIssuer, RunQuota: t.RunQuota, CostAllowance: t.CostAllowance,
		CreatedAtMs: t.CreatedAtMs,
	})
}

// tenantSlug normalizes a proposed tenant id to a safe slug: lowercased, with
// only [a-z0-9-_] retained. Empty when nothing valid remains.
func tenantSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}
