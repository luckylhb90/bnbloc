package shareagenthttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	tid := tenant.FromContext(r.Context())
	members, err := s.Tenants.ListMembers(r.Context(), tid)
	if err != nil {
		obs.ServerError(w, "list members failed", err)
		httpx.Error(w, "list members failed", http.StatusInternalServerError)
		return
	}
	views := make([]memberView, 0, len(members))
	for _, m := range members {
		views = append(views, memberView{
			UserID:     m.UserID,
			Role:       string(m.Role),
			JoinedAtMs: m.JoinedAtMs,
			Source:     string(m.Source),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"members": views})
}

type setRoleBody struct {
	Role string `json:"role"`
}

// handleTenantSetMemberRole updates one member's org role. It refuses to change
// the caller's own role (an admin cannot demote themselves and strand the org
// without an admin) and normalizes the role to a known value.
func (s *Service) handleTenantSetMemberRole(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		httpx.Unavailable(w, "tenant registry")
		return
	}
	userID := r.PathValue("userId")
	if userID == "" {
		httpx.Error(w, "missing user id", http.StatusBadRequest)
		return
	}
	if userID == callerSubject(r) {
		httpx.Error(w, "cannot change your own role", http.StatusForbidden)
		return
	}
	var body setRoleBody
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
		return
	}
	tid := tenant.FromContext(r.Context())
	existing, ok, err := s.Tenants.GetMember(r.Context(), tid, userID)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "member not found", http.StatusNotFound)
		return
	}
	newRole := store.NormalizeOrgRole(body.Role)
	strands, err := s.strandsLastAdmin(r.Context(), tid, existing, newRole)
	if err != nil {
		obs.ServerError(w, "list members failed", err)
		httpx.Error(w, "list members failed", http.StatusInternalServerError)
		return
	}
	if strands {
		httpx.Error(w, "cannot demote the last org admin", http.StatusConflict)
		return
	}
	fromRole := existing.Role
	existing.Role = newRole
	// An admin's role change takes the membership over from organization
	// sync: a manual row is never changed or removed by sync rules.
	existing.Source = store.MemberSourceManual
	if err := s.Tenants.PutMember(r.Context(), existing); err != nil {
		obs.ServerError(w, "update member failed", err)
		httpx.Error(w, "update member failed", http.StatusInternalServerError)
		return
	}
	s.auditMember(r, userID, "role", "role="+string(newRole)+" from="+string(fromRole))
	httpx.WriteJSON(w, http.StatusOK, memberView{
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	var body addMemberBody
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
		return
	}
	userID := strings.TrimSpace(body.UserID)
	if userID == "" {
		httpx.Error(w, "missing user id", http.StatusBadRequest)
		return
	}
	tid := tenant.FromContext(r.Context())
	existing, ok, err := s.Tenants.GetMember(r.Context(), tid, userID)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return
	}
	joined := existing.JoinedAtMs
	if !ok {
		joined = s.Now()
	}
	role := store.NormalizeOrgRole(body.Role)
	// Add-member upserts: on an existing member it is a role change, so it must
	// honor the same last-admin guard as the role-change path.
	strands, err := s.strandsLastAdmin(r.Context(), tid, existing, role)
	if err != nil {
		obs.ServerError(w, "list members failed", err)
		httpx.Error(w, "list members failed", http.StatusInternalServerError)
		return
	}
	if ok && strands {
		httpx.Error(w, "cannot demote the last org admin", http.StatusConflict)
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
		httpx.Error(w, "add member failed", http.StatusInternalServerError)
		return
	}
	if ok {
		s.auditMember(r, userID, "role", "role="+string(role)+" from="+string(existing.Role))
	} else {
		s.auditMember(r, userID, "add", "role="+string(role))
	}
	httpx.WriteJSON(w, http.StatusOK, memberView{UserID: m.UserID, Role: string(m.Role), JoinedAtMs: m.JoinedAtMs})
}

// handleTenantRemoveMember removes a member from the tenant. It refuses to
// remove the caller (an admin cannot remove themselves) and refuses to remove
// the last org_admin, which would strand the org without an administrator.
func (s *Service) handleTenantRemoveMember(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		httpx.Unavailable(w, "tenant registry")
		return
	}
	userID := r.PathValue("userId")
	if userID == "" {
		httpx.Error(w, "missing user id", http.StatusBadRequest)
		return
	}
	if userID == callerSubject(r) {
		httpx.Error(w, "cannot remove yourself", http.StatusForbidden)
		return
	}
	tid := tenant.FromContext(r.Context())
	target, ok, err := s.Tenants.GetMember(r.Context(), tid, userID)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "member not found", http.StatusNotFound)
		return
	}
	// Removal drops the member's role entirely ("" is never org_admin).
	strands, err := s.strandsLastAdmin(r.Context(), tid, target, "")
	if err != nil {
		obs.ServerError(w, "list members failed", err)
		httpx.Error(w, "list members failed", http.StatusInternalServerError)
		return
	}
	if strands {
		httpx.Error(w, "cannot remove the last org admin", http.StatusConflict)
		return
	}
	if err := s.Tenants.RemoveMember(r.Context(), tid, userID); err != nil {
		obs.ServerError(w, "remove member failed", err)
		httpx.Error(w, "remove member failed", http.StatusInternalServerError)
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"removed": true, "userId": userID})
}

// strandsLastAdmin reports whether moving member to newRole would leave the
// tenant without any org_admin: the member is currently an admin, is losing the
// role, and is the only admin. It is the single last-admin guard shared by
// role change, add-member upsert, and removal.
func (s *Service) strandsLastAdmin(ctx context.Context, tenantID string, member store.TenantMember, newRole store.OrgRole) (bool, error) {
	if member.Role != store.OrgRoleAdmin || newRole == store.OrgRoleAdmin {
		return false, nil
	}
	members, err := s.Tenants.ListMembers(ctx, tenantID)
	if err != nil {
		return false, err
	}
	admins := 0
	for _, m := range members {
		if m.Role == store.OrgRoleAdmin {
			admins++
		}
	}
	return admins <= 1, nil
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	var body quotaBody
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
		return
	}
	if body.RunQuota < 0 || body.CostAllowance < 0 {
		httpx.Error(w, "quota must be non-negative", http.StatusBadRequest)
		return
	}
	tid := tenant.FromContext(r.Context())
	if err := s.Tenants.SetQuota(r.Context(), tid, body.RunQuota, body.CostAllowance); err != nil {
		obs.ServerError(w, "set quota failed", err)
		httpx.Error(w, "set quota failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, quotaBody{RunQuota: body.RunQuota, CostAllowance: body.CostAllowance})
}

type settingsView struct {
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	SSOIssuer string `json:"ssoIssuer"`
	// RequireSandboxForBorrow: borrowed agent drives must run in the owner's
	// agent sandbox (refused when it is unavailable).
	RequireSandboxForBorrow bool `json:"requireSandboxForBorrow"`
	// OutsideLinksDisabled: share links for people outside the
	// organization ("signed_in" and "anyone") are off; AnonymousLinksDisabled:
	// links anyone can open without signing in ("anyone") are off. A
	// disabled kind cannot be created or switched to, and existing ones
	// resolve 404; links for the organization's members stay allowed
	// (store.Tenant.ShareAccessAllowed).
	OutsideLinksDisabled   bool `json:"outsideLinksDisabled"`
	AnonymousLinksDisabled bool `json:"anonymousLinksDisabled"`
	// PublishApproval: whether a member's publish to the organization
	// waits for an org admin ("required", the default) or takes effect at
	// once ("none"); contentshare.NeedsApproval reads it.
	PublishApproval string `json:"publishApproval"`
}

func viewSettings(t store.Tenant) settingsView {
	return settingsView{
		TenantID:                t.TenantID,
		Name:                    t.Name,
		Status:                  string(t.Status),
		SSOIssuer:               t.SSOIssuer,
		RequireSandboxForBorrow: t.RequireSandboxForBorrow,
		OutsideLinksDisabled:    t.OutsideLinksDisabled,
		AnonymousLinksDisabled:  t.AnonymousLinksDisabled,
		PublishApproval:         t.PublishApproval,
	}
}

// handleTenantSettings returns the ctx tenant's registry row (name, status, SSO
// issuer/domain) for the org settings tab.
func (s *Service) handleTenantSettings(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		httpx.Unavailable(w, "tenant registry")
		return
	}
	tid := tenant.FromContext(r.Context())
	t, err := s.Tenants.GetTenant(r.Context(), tid)
	if err != nil {
		httpx.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewSettings(t))
}

type settingsBody struct {
	Name                    *string `json:"name"`
	SSOIssuer               *string `json:"ssoIssuer"`
	RequireSandboxForBorrow *bool   `json:"requireSandboxForBorrow"`
	OutsideLinksDisabled    *bool   `json:"outsideLinksDisabled"`
	AnonymousLinksDisabled  *bool   `json:"anonymousLinksDisabled"`
	PublishApproval         *string `json:"publishApproval"`
}

// handleTenantUpdateSettings updates the tenant's display name, SSO
// issuer/domain and policy switches (sandbox for borrowed drives, share
// links for people outside the organization and without sign-in, publish
// approval). Only provided fields change; the tenant id and
// status are not editable here.
func (s *Service) handleTenantUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		httpx.Unavailable(w, "tenant registry")
		return
	}
	var body settingsBody
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
		return
	}
	if body.PublishApproval != nil && !wire.ValidPublishApproval(*body.PublishApproval) {
		httpx.Error(w, "publishApproval must be \""+wire.PublishApprovalRequired+"\" or \""+wire.PublishApprovalNone+"\"", http.StatusBadRequest)
		return
	}
	tid := tenant.FromContext(r.Context())
	t, err := s.Tenants.GetTenant(r.Context(), tid)
	if err != nil {
		httpx.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	if body.Name != nil {
		t.Name = *body.Name
	}
	if body.SSOIssuer != nil {
		t.SSOIssuer = *body.SSOIssuer
	}
	if err := s.Tenants.PutTenant(r.Context(), t); errors.Is(err, store.ErrSSOIssuerTaken) {
		httpx.Error(w, "sso issuer already maps another organization", http.StatusConflict)
		return
	} else if err != nil {
		obs.ServerError(w, "update settings failed", err)
		httpx.Error(w, "update settings failed", http.StatusInternalServerError)
		return
	}
	if body.RequireSandboxForBorrow != nil || body.OutsideLinksDisabled != nil || body.AnonymousLinksDisabled != nil || body.PublishApproval != nil {
		t, err = s.Tenants.SetTenantPolicy(r.Context(), tid, store.TenantPolicyUpdate{
			RequireSandboxForBorrow: body.RequireSandboxForBorrow,
			OutsideLinksDisabled:    body.OutsideLinksDisabled,
			AnonymousLinksDisabled:  body.AnonymousLinksDisabled,
			PublishApproval:         body.PublishApproval,
		})
		if err != nil {
			obs.ServerError(w, "update tenant policy failed", err)
			httpx.Error(w, "update settings failed", http.StatusInternalServerError)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, viewSettings(t))
}
