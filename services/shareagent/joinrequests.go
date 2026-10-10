package shareagenthttp

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Join requests: the self-service path for a user to join a tenant. A user
// browses the tenant directory and submits a request; a tenant admin reviews
// pending requests and approves (creating a membership) or rejects. Approval is
// what lets the user's next login land in that tenant (provisioning falls back
// to their membership when no SSO mapping applies).

// directoryTenantView is one joinable tenant in the public directory. It
// deliberately exposes only non-sensitive fields (id, name) plus the caller's
// own relationship (member / pending / none) so a user knows what to do next.
type directoryTenantView struct {
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	Relation  string `json:"relation"` // "member" | "pending" | "rejected" | "none"
	CanReview bool   `json:"-"`
}

// handleTenantDirectory lists active tenants a user may request to join, with
// the caller's current relation to each. Any authenticated user may read it;
// suspended tenants are hidden. The caller is the X-Bnbloc-User subject.
func (s *Service) handleTenantDirectory(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	caller := callerSubject(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	out := make([]directoryTenantView, 0)
	for _, t := range s.Tenants.ListTenants(r.Context()) {
		if t.Status != store.TenantActive {
			continue
		}
		relation := "none"
		if _, ok := s.Tenants.GetMember(r.Context(), t.TenantID, caller); ok {
			relation = "member"
		} else if jr, ok := s.Tenants.GetJoinRequest(r.Context(), t.TenantID, caller); ok {
			relation = string(jr.Status)
		}
		out = append(out, directoryTenantView{TenantID: t.TenantID, Name: t.Name, Relation: relation})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out})
}

type joinRequestBody struct {
	Message string `json:"message"`
}

// handleCreateJoinRequest submits (or re-submits) the caller's request to join
// the tenant named in the path. Refuses when the caller is already a member. A
// prior rejected/pending request is overwritten with a fresh pending one so a
// user can re-apply. The path tenant is explicit (not the ctx tenant) because a
// user requesting to join is, by definition, not yet scoped to it.
func (s *Service) handleCreateJoinRequest(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	caller := callerSubject(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	tid := r.PathValue("tenantId")
	t, err := s.Tenants.GetTenant(r.Context(), tid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "tenant not found")
		return
	}
	if t.Status != store.TenantActive {
		writeErr(w, http.StatusConflict, "tenant is not accepting requests")
		return
	}
	if _, ok := s.Tenants.GetMember(r.Context(), tid, caller); ok {
		writeErr(w, http.StatusConflict, "already a member")
		return
	}
	var body joinRequestBody
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	jr := store.JoinRequest{
		TenantID:    tid,
		UserID:      caller,
		Message:     strings.TrimSpace(body.Message),
		Status:      store.JoinPending,
		CreatedAtMs: s.Now(),
	}
	if err := s.Tenants.PutJoinRequest(r.Context(), jr); err != nil {
		obs.ServerError(w, "submit request failed", err)
		writeErr(w, http.StatusInternalServerError, "submit request failed")
		return
	}
	writeJSON(w, http.StatusCreated, joinRequestView(jr))
}

// handleMyJoinRequests lists the caller's own join requests across tenants so a
// user can track status.
func (s *Service) handleMyJoinRequests(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	caller := callerSubject(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	reqs := s.Tenants.ListJoinRequestsForUser(r.Context(), caller)
	views := make([]map[string]any, 0, len(reqs))
	for _, jr := range reqs {
		views = append(views, joinRequestView(jr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": views})
}

// handleListJoinRequests lists the ctx tenant's join requests for the admin
// review queue. Defaults to pending; ?status=all|approved|rejected widens it.
// RBAC (manage_org) is enforced at the gateway; the tenant is the ctx tenant.
func (s *Service) handleListJoinRequests(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	tid := tenant.FromContext(r.Context())
	status := store.JoinPending
	switch strings.TrimSpace(r.URL.Query().Get("status")) {
	case "all":
		status = ""
	case "approved":
		status = store.JoinApproved
	case "rejected":
		status = store.JoinRejected
	}
	reqs := s.Tenants.ListJoinRequests(r.Context(), tid, status)
	views := make([]map[string]any, 0, len(reqs))
	for _, jr := range reqs {
		views = append(views, joinRequestView(jr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": views})
}

// handleApproveJoinRequest approves a pending request: it creates the tenant
// membership (as a plain member) and marks the request approved. Idempotent for
// an already-approved request. The decider is the admin caller.
func (s *Service) handleApproveJoinRequest(w http.ResponseWriter, r *http.Request) {
	s.decideJoinRequest(w, r, true)
}

// handleRejectJoinRequest rejects a pending request without creating a
// membership. The user may re-apply later.
func (s *Service) handleRejectJoinRequest(w http.ResponseWriter, r *http.Request) {
	s.decideJoinRequest(w, r, false)
}

func (s *Service) decideJoinRequest(w http.ResponseWriter, r *http.Request, approve bool) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	tid := tenant.FromContext(r.Context())
	userID := r.PathValue("userId")
	jr, ok := s.Tenants.GetJoinRequest(r.Context(), tid, userID)
	if !ok {
		writeErr(w, http.StatusNotFound, "join request not found")
		return
	}
	now := s.Now()
	if approve {
		// Create the membership first so approval is meaningful even if the
		// status write is retried; PutMember is idempotent.
		if err := s.Tenants.PutMember(r.Context(), store.TenantMember{
			TenantID: tid, UserID: userID, Role: store.OrgRoleMember, JoinedAtMs: now,
		}); err != nil {
			obs.ServerError(w, "approve failed", err)
			writeErr(w, http.StatusInternalServerError, "approve failed")
			return
		}
		jr.Status = store.JoinApproved
	} else {
		jr.Status = store.JoinRejected
	}
	jr.DecidedAtMs = now
	jr.DecidedBy = callerSubject(r)
	if err := s.Tenants.PutJoinRequest(r.Context(), jr); err != nil {
		obs.ServerError(w, "update request failed", err)
		writeErr(w, http.StatusInternalServerError, "update request failed")
		return
	}
	s.auditMember(r, userID, "join_"+string(jr.Status), "")
	writeJSON(w, http.StatusOK, joinRequestView(jr))
}

// joinRequestView is the JSON projection of a join request.
func joinRequestView(jr store.JoinRequest) map[string]any {
	return map[string]any{
		"tenantId":    jr.TenantID,
		"userId":      jr.UserID,
		"message":     jr.Message,
		"status":      string(jr.Status),
		"createdAtMs": jr.CreatedAtMs,
		"decidedAtMs": jr.DecidedAtMs,
		"decidedBy":   jr.DecidedBy,
	}
}
