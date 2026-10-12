package shareagenthttp

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	caller := callerSubject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	tenants, err := s.Tenants.ListTenants(r.Context())
	if err != nil {
		obs.ServerError(w, "list tenants failed", err)
		httpx.Error(w, "list tenants failed", http.StatusInternalServerError)
		return
	}
	out := make([]directoryTenantView, 0)
	for _, t := range tenants {
		if t.Status != store.TenantActive {
			continue
		}
		relation := "none"
		_, member, err := s.Tenants.GetMember(r.Context(), t.TenantID, caller)
		if err != nil {
			obs.ServerError(w, "read membership failed", err)
			httpx.Error(w, "read membership failed", http.StatusInternalServerError)
			return
		}
		jr, requested, err := s.Tenants.GetJoinRequest(r.Context(), t.TenantID, caller)
		if err != nil {
			obs.ServerError(w, "read join request failed", err)
			httpx.Error(w, "read join request failed", http.StatusInternalServerError)
			return
		}
		if member {
			relation = "member"
		} else if requested {
			relation = string(jr.Status)
		}
		out = append(out, directoryTenantView{TenantID: t.TenantID, Name: t.Name, Relation: relation})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tenants": out})
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	caller := callerSubject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	tid := r.PathValue("tenantId")
	t, err := s.Tenants.GetTenant(r.Context(), tid)
	if err != nil {
		httpx.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	if t.Status != store.TenantActive {
		httpx.Error(w, "tenant is not accepting requests", http.StatusConflict)
		return
	}
	_, member, err := s.Tenants.GetMember(r.Context(), tid, caller)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return
	}
	if member {
		httpx.Error(w, "already a member", http.StatusConflict)
		return
	}
	var body joinRequestBody
	if !httpx.DecodeOptionalJSON(w, r, httpx.DefaultBodyLimit, &body) {
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
		httpx.Error(w, "submit request failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, joinRequestView(jr))
}

// handleMyJoinRequests lists the caller's own join requests across tenants so a
// user can track status.
func (s *Service) handleMyJoinRequests(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		httpx.Unavailable(w, "tenant registry")
		return
	}
	caller := callerSubject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	reqs, err := s.Tenants.ListJoinRequestsForUser(r.Context(), caller)
	if err != nil {
		obs.ServerError(w, "list join requests failed", err)
		httpx.Error(w, "list join requests failed", http.StatusInternalServerError)
		return
	}
	views := make([]map[string]any, 0, len(reqs))
	for _, jr := range reqs {
		views = append(views, joinRequestView(jr))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"requests": views})
}

// handleListJoinRequests lists the ctx tenant's join requests for the admin
// review queue. Defaults to pending; ?status=all|approved|rejected widens it.
// RBAC (manage_org) is enforced at the gateway; the tenant is the ctx tenant.
func (s *Service) handleListJoinRequests(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		httpx.Unavailable(w, "tenant registry")
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
	reqs, err := s.Tenants.ListJoinRequests(r.Context(), tid, status)
	if err != nil {
		obs.ServerError(w, "list join requests failed", err)
		httpx.Error(w, "list join requests failed", http.StatusInternalServerError)
		return
	}
	views := make([]map[string]any, 0, len(reqs))
	for _, jr := range reqs {
		views = append(views, joinRequestView(jr))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"requests": views})
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
		httpx.Unavailable(w, "tenant registry")
		return
	}
	tid := tenant.FromContext(r.Context())
	userID := r.PathValue("userId")
	jr, ok, err := s.Tenants.GetJoinRequest(r.Context(), tid, userID)
	if err != nil {
		obs.ServerError(w, "read join request failed", err)
		httpx.Error(w, "read join request failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "join request not found", http.StatusNotFound)
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
			httpx.Error(w, "approve failed", http.StatusInternalServerError)
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
		httpx.Error(w, "update request failed", http.StatusInternalServerError)
		return
	}
	s.auditMember(r, userID, "join_"+string(jr.Status), "")
	httpx.WriteJSON(w, http.StatusOK, joinRequestView(jr))
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
