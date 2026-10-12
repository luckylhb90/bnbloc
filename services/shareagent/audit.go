package shareagenthttp

import (
	"net/http"
	"strconv"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// auditRecordView is the JSON projection of one org-wide audit record. It omits
// the internal grant id and tenant id; the tenant is implied by the caller.
type auditRecordView struct {
	// ID with AtMs is the record's cursor (?before=&beforeId=).
	ID          int64  `json:"id"`
	RoomID      string `json:"roomId"`
	ActorUserID string `json:"actorUserId"`
	OwnerUserID string `json:"ownerUserId"`
	Action      string `json:"action"`
	Decision    string `json:"decision"`
	DenyReason  string `json:"denyReason"`
	Model       string `json:"model"`
	ChannelID   string `json:"channelId,omitempty"`
	AtMs        int64  `json:"atMs"`
}

// handleTenantAudit returns the ctx tenant's shared-agent audit records across
// ALL rooms newest-first, for an org-wide audit trail. The tenant is bound to
// the request context by svcauth; RBAC gating (org_admin only) is enforced at
// the gateway. Optional query params: limit (<=0 defaults 100, capped 500) and
// before (cursor: keep only records strictly older than that at_ms; with
// beforeId, the records after that record in the audit order, so records
// sharing a millisecond are never skipped).
func (s *Service) handleTenantAudit(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	q := store.AuditRecordQuery{Limit: limit}
	if v := r.URL.Query().Get("before"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			after := store.AuditCursor{AtMs: n, Source: store.AuditSourceBorrow}
			if id, err := strconv.ParseInt(r.URL.Query().Get("beforeId"), 10, 64); err == nil {
				after.ID = id
			}
			q.After = &after
		}
	}
	recs, err := s.Grants.AuditRecords(r.Context(), q)
	if err != nil {
		obs.ServerError(w, "read audit failed", err)
		httpx.Error(w, "read audit failed", http.StatusInternalServerError)
		return
	}
	views := make([]auditRecordView, 0, len(recs))
	for _, rec := range recs {
		views = append(views, auditRecordView{
			ID:          rec.ID,
			RoomID:      rec.RoomID,
			ActorUserID: rec.ActorUserID,
			OwnerUserID: rec.OwnerUserID,
			Action:      rec.Action,
			Decision:    rec.Decision,
			DenyReason:  rec.DenyReason,
			Model:       rec.Model,
			ChannelID:   rec.ChannelID,
			AtMs:        rec.AtMs,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"records": views})
}
