package shareagenthttp

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handleTenantRevokeMemberSessions is the org admin's "sign out member
// everywhere": it revokes every refresh family of a member of the ctx
// tenant, so none of their clients (web, desktop, CLI, bridge) can renew its
// session. A refresh family is the sign-in, not a tenant scope — one sign-in
// reaches every tenant the subject belongs to via tenant switching — so the
// revocation covers all of the member's families. Live session bearers lapse
// within the session TTL.
func (s *Service) handleTenantRevokeMemberSessions(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil || s.RefreshTokens == nil {
		httpx.Unavailable(w, "session revocation")
		return
	}
	userID := r.PathValue("userId")
	if userID == "" {
		httpx.Error(w, "missing user id", http.StatusBadRequest)
		return
	}
	tid := tenant.FromContext(r.Context())
	_, member, err := s.Tenants.GetMember(r.Context(), tid, userID)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return
	}
	if !member {
		httpx.Error(w, "member not found", http.StatusNotFound)
		return
	}
	n, err := s.RefreshTokens.RevokeRefreshFamilies(r.Context(), store.RefreshRevokeFilter{Subject: userID}, s.nowMs())
	if err != nil {
		obs.ServerError(w, "revoke sessions failed", err)
		httpx.Error(w, "revoke sessions failed", http.StatusInternalServerError)
		return
	}
	log.Printf("bnbloc-shareagent: %s revoked %d session(s) of %s (tenant %s)", callerSubject(r), n, userID, tid)
	s.audit(r.Context(), store.ExtAuditRecord{
		Kind: wire.AuditKindSession, Name: userID, Action: "revoke_member", Actor: callerSubject(r),
		TenantID: tid, Detail: "revoked=" + strconv.Itoa(n),
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"userId": userID, "revoked": n})
}

// revokeSessionsFor revokes refresh families matching filter after an
// authorization change that already committed (member removed, tenant
// suspended). Best-effort: the change itself stands even if revocation
// fails — the gateway's refresh re-checks the suspension gate anyway — but
// the failure is logged loudly.
func (s *Service) revokeSessionsFor(ctx context.Context, filter store.RefreshRevokeFilter, reason string) {
	if s.RefreshTokens == nil {
		return
	}
	n, err := s.RefreshTokens.RevokeRefreshFamilies(ctx, filter, s.nowMs())
	if err != nil {
		log.Printf("bnbloc-shareagent: WARNING %s: revoking sessions (subject=%q tenant=%q) failed: %v", reason, filter.Subject, filter.TenantID, err)
		return
	}
	if n > 0 {
		log.Printf("bnbloc-shareagent: %s: revoked %d session(s) (subject=%q tenant=%q)", reason, n, filter.Subject, filter.TenantID)
		// The tenant whose admin sees this: a suspension names it in the
		// filter; a member removal is in the ctx tenant.
		tid := filter.TenantID
		if tid == "" {
			tid = tenant.FromContext(ctx)
		}
		s.audit(ctx, store.ExtAuditRecord{
			Kind: wire.AuditKindSession, Name: filter.Subject, Action: "revoke", TenantID: tid,
			Detail: reason + ": revoked=" + strconv.Itoa(n),
		})
	}
}

// suspendedTenantRevokeFilter selects the sessions a tenant suspension ends:
// every family whose session landed in the tenant, except platform
// super-admins (they keep access to administer the suspended org).
func suspendedTenantRevokeFilter(tenantID string) store.RefreshRevokeFilter {
	return store.RefreshRevokeFilter{TenantID: tenantID, ExceptRole: string(rbac.RoleSuperAdmin)}
}

// nowMs is the service clock in unix millis.
func (s *Service) nowMs() int64 {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UnixMilli()
}
