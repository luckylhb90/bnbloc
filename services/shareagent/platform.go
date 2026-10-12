package shareagenthttp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	tenants, err := s.Tenants.ListTenants(r.Context())
	if err != nil {
		obs.ServerError(w, "list tenants failed", err)
		httpx.Error(w, "list tenants failed", http.StatusInternalServerError)
		return
	}
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tenants": views})
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	var body createTenantBody
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
		return
	}
	id := tenantSlug(body.TenantID)
	if id == "" {
		httpx.Error(w, "tenant id must be a non-empty slug (a-z 0-9 - _)", http.StatusBadRequest)
		return
	}
	if _, err := s.Tenants.GetTenant(r.Context(), id); err == nil {
		httpx.Error(w, "tenant already exists", http.StatusConflict)
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
	if err := s.Tenants.PutTenant(r.Context(), t); errors.Is(err, store.ErrSSOIssuerTaken) {
		httpx.Error(w, "sso issuer already maps another organization", http.StatusConflict)
		return
	} else if err != nil {
		obs.ServerError(w, "create tenant failed", err)
		httpx.Error(w, "create tenant failed", http.StatusInternalServerError)
		return
	}
	s.auditTenant(r, id, "create", "name="+name)
	httpx.WriteJSON(w, http.StatusCreated, platformTenantView{
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	id := r.PathValue("tenantId")
	var body statusBody
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
		return
	}
	status := store.TenantStatus(strings.TrimSpace(body.Status))
	if status != store.TenantActive && status != store.TenantSuspended {
		httpx.Error(w, "status must be active or suspended", http.StatusBadRequest)
		return
	}
	if id == tenant.Default && status == store.TenantSuspended {
		httpx.Error(w, "the default tenant cannot be suspended", http.StatusConflict)
		return
	}
	t, err := s.Tenants.GetTenant(r.Context(), id)
	if err != nil {
		httpx.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	t.Status = status
	if err := s.Tenants.PutTenant(r.Context(), t); err != nil {
		obs.ServerError(w, "update status failed", err)
		httpx.Error(w, "update status failed", http.StatusInternalServerError)
		return
	}
	s.auditTenant(r, id, "status", "status="+string(status))
	if status == store.TenantSuspended {
		s.revokeSessionsFor(r.Context(), suspendedTenantRevokeFilter(id), "tenant "+id+" suspended")
	}
	httpx.WriteJSON(w, http.StatusOK, platformTenantView{
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
