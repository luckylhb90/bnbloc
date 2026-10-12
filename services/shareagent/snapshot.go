package shareagenthttp

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// sharedAccess is the credential-free access snapshot projected to clients. It
// mirrors AgentGUISharedAgentAccess and never contains a credential field.
type sharedAccess struct {
	GrantID               string               `json:"grantId"`
	OwnerUserID           string               `json:"ownerUserId"`
	OwnerOnline           bool                 `json:"ownerOnline"`
	RunQuotaRemaining     *int                 `json:"runQuotaRemaining,omitempty"`
	ResetAtUnixMs         *int64               `json:"resetAtUnixMs,omitempty"`
	CostAllowance         *float64             `json:"costAllowance,omitempty"`
	ActiveSessions        int                  `json:"activeSessions"`
	MaxConcurrentSessions int                  `json:"maxConcurrentSessions"`
	AllowedModelRange     []store.AllowedModel `json:"allowedModelRange"`
	Capabilities          store.Capabilities   `json:"capabilities"`
	AuditRequired         bool                 `json:"auditRequired"`
	ExpiresAtMs           *int64               `json:"expiresAtMs,omitempty"`
}

// handleAccess projects the credential-free sharedAccess snapshot. The snapshot
// is presentation + defense-in-depth, never authority: every lifecycle action
// still re-reads current state through the controller.
func (s *Service) handleAccess(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := s.requireMember(w, r, roomID, callerSubject(r), membership.AnyMember); !ok {
		return
	}
	grantID := r.URL.Query().Get("grantId")
	grant, err := s.Grants.GetGrant(r.Context(), grantID)
	if err != nil || grant.RoomID != roomID {
		httpx.Error(w, "grant not found", http.StatusNotFound)
		return
	}
	snapshot := sharedAccess{
		GrantID:               grant.GrantID,
		OwnerUserID:           grant.OwnerUserID,
		OwnerOnline:           s.Presence.IsOwnerOnline(roomID, grant.OwnerUserID),
		ResetAtUnixMs:         grant.ResetAtMs,
		CostAllowance:         grant.CostAllowance,
		ActiveSessions:        s.Grants.ActiveLeases(r.Context(), grant.GrantID),
		MaxConcurrentSessions: grant.MaxConcurrent,
		AllowedModelRange:     grant.AllowedModels,
		Capabilities:          grant.Capabilities,
		AuditRequired:         true,
		ExpiresAtMs:           grant.ExpiresAtMs,
	}
	if grant.RunQuota != nil {
		remaining := *grant.RunQuota - grant.RunsUsed
		if remaining < 0 {
			remaining = 0
		}
		snapshot.RunQuotaRemaining = &remaining
	}
	httpx.WriteJSON(w, http.StatusOK, snapshot)
}

func callerSubject(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
}
