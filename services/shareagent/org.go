package shareagenthttp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// memberView is the JSON projection of one tenant member for the org console.
type memberView struct {
	UserID     string `json:"userId"`
	Role       string `json:"role"`
	JoinedAtMs int64  `json:"joinedAtMs"`
	// Source is manual or sync (organization sync owns the row).
	Source string `json:"source,omitempty"`
}

// handleTenantMembers lists the ctx tenant's members newest-join-first is not
// required; the store returns join order. The tenant is bound by svcauth and
// RBAC (manage_org) is enforced at the gateway.
func (s *Service) handleTenantMembers(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	tid := tenant.FromContext(r.Context())
	members := s.Tenants.ListMembers(r.Context(), tid)
	views := make([]memberView, 0, len(members))
	for _, m := range members {
		views = append(views, memberView{
			UserID:     m.UserID,
			Role:       string(m.Role),
			JoinedAtMs: m.JoinedAtMs,
			Source:     string(m.Source),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": views})
}

type setRoleBody struct {
	Role string `json:"role"`
}

// handleTenantSetMemberRole updates one member's org role. It refuses to change
// the caller's own role (an admin cannot demote themselves and strand the org
// without an admin) and normalizes the role to a known value.
func (s *Service) handleTenantSetMemberRole(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	userID := r.PathValue("userId")
	if userID == "" {
		writeErr(w, http.StatusBadRequest, "missing user id")
		return
	}
	if userID == callerSubject(r) {
		writeErr(w, http.StatusForbidden, "cannot change your own role")
		return
	}
	var body setRoleBody
	if !decodeJSON(w, r, &body) {
		return
	}
	tid := tenant.FromContext(r.Context())
	existing, ok := s.Tenants.GetMember(r.Context(), tid, userID)
	if !ok {
		writeErr(w, http.StatusNotFound, "member not found")
		return
	}
	newRole := store.NormalizeOrgRole(body.Role)
	if s.strandsLastAdmin(r.Context(), tid, existing, newRole) {
		writeErr(w, http.StatusConflict, "cannot demote the last org admin")
		return
	}
	fromRole := existing.Role
	existing.Role = newRole
	// An admin's role change takes the membership over from organization
	// sync: a manual row is never changed or removed by sync rules.
	existing.Source = store.MemberSourceManual
	if err := s.Tenants.PutMember(r.Context(), existing); err != nil {
		obs.ServerError(w, "update member failed", err)
		writeErr(w, http.StatusInternalServerError, "update member failed")
		return
	}
	s.auditMember(r, userID, "role", "role="+string(newRole)+" from="+string(fromRole))
	writeJSON(w, http.StatusOK, memberView{
		UserID:     existing.UserID,
		Role:       string(existing.Role),
		JoinedAtMs: existing.JoinedAtMs,
	})
}

type addMemberBody struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
}

// handleTenantAddMember directly adds (or updates) a member of the tenant by
// their subject id, with a chosen org role. This is the admin-driven path
// (distinct from login-time auto-provisioning) so an org_admin can seed members
// before their first sign-in.
func (s *Service) handleTenantAddMember(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	var body addMemberBody
	if !decodeJSON(w, r, &body) {
		return
	}
	userID := strings.TrimSpace(body.UserID)
	if userID == "" {
		writeErr(w, http.StatusBadRequest, "missing user id")
		return
	}
	tid := tenant.FromContext(r.Context())
	existing, ok := s.Tenants.GetMember(r.Context(), tid, userID)
	joined := existing.JoinedAtMs
	if !ok {
		joined = s.Now()
	}
	role := store.NormalizeOrgRole(body.Role)
	// Add-member upserts: on an existing member it is a role change, so it must
	// honor the same last-admin guard as the role-change path.
	if ok && s.strandsLastAdmin(r.Context(), tid, existing, role) {
		writeErr(w, http.StatusConflict, "cannot demote the last org admin")
		return
	}
	m := store.TenantMember{
		TenantID:   tid,
		UserID:     userID,
		Role:       role,
		JoinedAtMs: joined,
	}
	if err := s.Tenants.PutMember(r.Context(), m); err != nil {
		obs.ServerError(w, "add member failed", err)
		writeErr(w, http.StatusInternalServerError, "add member failed")
		return
	}
	if ok {
		s.auditMember(r, userID, "role", "role="+string(role)+" from="+string(existing.Role))
	} else {
		s.auditMember(r, userID, "add", "role="+string(role))
	}
	writeJSON(w, http.StatusOK, memberView{UserID: m.UserID, Role: string(m.Role), JoinedAtMs: m.JoinedAtMs})
}

// handleTenantRemoveMember removes a member from the tenant. It refuses to
// remove the caller (an admin cannot remove themselves) and refuses to remove
// the last org_admin, which would strand the org without an administrator.
func (s *Service) handleTenantRemoveMember(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	userID := r.PathValue("userId")
	if userID == "" {
		writeErr(w, http.StatusBadRequest, "missing user id")
		return
	}
	if userID == callerSubject(r) {
		writeErr(w, http.StatusForbidden, "cannot remove yourself")
		return
	}
	tid := tenant.FromContext(r.Context())
	target, ok := s.Tenants.GetMember(r.Context(), tid, userID)
	if !ok {
		writeErr(w, http.StatusNotFound, "member not found")
		return
	}
	// Removal drops the member's role entirely ("" is never org_admin).
	if s.strandsLastAdmin(r.Context(), tid, target, "") {
		writeErr(w, http.StatusConflict, "cannot remove the last org admin")
		return
	}
	if err := s.Tenants.RemoveMember(r.Context(), tid, userID); err != nil {
		obs.ServerError(w, "remove member failed", err)
		writeErr(w, http.StatusInternalServerError, "remove member failed")
		return
	}
	s.auditMember(r, userID, "remove", "role="+string(target.Role))
	// A removed member must sign in again: their refresh families can no
	// longer renew a session that switch-tenant could re-scope into tid.
	s.revokeSessionsFor(r.Context(), store.RefreshRevokeFilter{Subject: userID}, "member removed from "+tid)
	if s.PushSubscriptions != nil {
		if err := s.PushSubscriptions.DeleteWebPushSubscriptionsForUser(r.Context(), userID); err != nil {
			slog.Warn("bnbloc shareagent: delete removed member's push subscriptions failed",
				"event", "bnbloc.shareagent.push_subscriptions_cleanup_failed", "tenant", tid, "user", userID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true, "userId": userID})
}

// strandsLastAdmin reports whether moving member to newRole would leave the
// tenant without any org_admin: the member is currently an admin, is losing the
// role, and is the only admin. It is the single last-admin guard shared by
// role change, add-member upsert, and removal.
func (s *Service) strandsLastAdmin(ctx context.Context, tenantID string, member store.TenantMember, newRole store.OrgRole) bool {
	if member.Role != store.OrgRoleAdmin || newRole == store.OrgRoleAdmin {
		return false
	}
	admins := 0
	for _, m := range s.Tenants.ListMembers(ctx, tenantID) {
		if m.Role == store.OrgRoleAdmin {
			admins++
		}
	}
	return admins <= 1
}

type quotaBody struct {
	RunQuota      int     `json:"runQuota"`
	CostAllowance float64 `json:"costAllowance"`
}

// handleTenantSetQuota persists the ctx tenant's org-level ceilings
// (0 = unlimited). Enforcement reads the persisted row per request, so the
// change takes effect immediately for this tenant only.
func (s *Service) handleTenantSetQuota(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	var body quotaBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.RunQuota < 0 || body.CostAllowance < 0 {
		writeErr(w, http.StatusBadRequest, "quota must be non-negative")
		return
	}
	tid := tenant.FromContext(r.Context())
	if err := s.Tenants.SetQuota(r.Context(), tid, body.RunQuota, body.CostAllowance); err != nil {
		obs.ServerError(w, "set quota failed", err)
		writeErr(w, http.StatusInternalServerError, "set quota failed")
		return
	}
	writeJSON(w, http.StatusOK, quotaBody{RunQuota: body.RunQuota, CostAllowance: body.CostAllowance})
}

type settingsView struct {
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	SSOIssuer string `json:"ssoIssuer"`
	// RequireSandboxForBorrow: borrowed agent drives must run in the owner's
	// agent sandbox (refused when it is unavailable).
	RequireSandboxForBorrow bool `json:"requireSandboxForBorrow"`
	// PublicSharesDisabled: public artifact share links are off (create is
	// refused, existing links resolve 404).
	PublicSharesDisabled bool `json:"publicSharesDisabled"`
}

func viewSettings(t store.Tenant) settingsView {
	return settingsView{
		TenantID:                t.TenantID,
		Name:                    t.Name,
		Status:                  string(t.Status),
		SSOIssuer:               t.SSOIssuer,
		RequireSandboxForBorrow: t.RequireSandboxForBorrow,
		PublicSharesDisabled:    t.PublicSharesDisabled,
	}
}

// handleTenantSettings returns the ctx tenant's registry row (name, status, SSO
// issuer/domain) for the org settings tab.
func (s *Service) handleTenantSettings(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	tid := tenant.FromContext(r.Context())
	t, err := s.Tenants.GetTenant(r.Context(), tid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, viewSettings(t))
}

type settingsBody struct {
	Name                    *string `json:"name"`
	SSOIssuer               *string `json:"ssoIssuer"`
	RequireSandboxForBorrow *bool   `json:"requireSandboxForBorrow"`
	PublicSharesDisabled    *bool   `json:"publicSharesDisabled"`
}

// handleTenantUpdateSettings updates the tenant's display name, SSO
// issuer/domain and policy switches (sandbox for borrowed drives, public share
// links). Only provided fields change; the tenant id and status are not
// editable here.
func (s *Service) handleTenantUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	var body settingsBody
	if !decodeJSON(w, r, &body) {
		return
	}
	tid := tenant.FromContext(r.Context())
	t, err := s.Tenants.GetTenant(r.Context(), tid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "tenant not found")
		return
	}
	if body.Name != nil {
		t.Name = *body.Name
	}
	if body.SSOIssuer != nil {
		t.SSOIssuer = *body.SSOIssuer
	}
	if err := s.Tenants.PutTenant(r.Context(), t); err != nil {
		obs.ServerError(w, "update settings failed", err)
		writeErr(w, http.StatusInternalServerError, "update settings failed")
		return
	}
	if body.RequireSandboxForBorrow != nil || body.PublicSharesDisabled != nil {
		t, err = s.Tenants.SetTenantPolicy(r.Context(), tid, store.TenantPolicyUpdate{
			RequireSandboxForBorrow: body.RequireSandboxForBorrow,
			PublicSharesDisabled:    body.PublicSharesDisabled,
		})
		if err != nil {
			obs.ServerError(w, "update tenant policy failed", err)
			writeErr(w, http.StatusInternalServerError, "update settings failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, viewSettings(t))
}
