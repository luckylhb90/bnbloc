package shareagenthttp

import (
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// sharedAgentEntry is one borrowable shared agent projected to room members for
// discovery. It carries no credential and no owner endpoint, only what a
// browser client needs to decide whether to borrow.
type sharedAgentEntry struct {
	TargetID      string   `json:"targetId"`
	OwnerUserID   string   `json:"ownerUserId"`
	Provider      string   `json:"provider"`
	AllowedModels []string `json:"allowedModels"`
	HasQuota      bool     `json:"hasQuota"`
}

// handleListSharedAgents lists the ACTIVE (non-revoked, non-expired)
// authorizations in a room so browser users with no local core can discover
// borrowable agents. It is membership-checked: only a room member may list.
func (s *Service) handleListSharedAgents(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.requireMember(w, roomID, callerSubject(r), tenant.FromContext(r.Context())) {
		return
	}
	now := s.Now()
	entries := []sharedAgentEntry{}
	for _, g := range s.Grants.RoomGrants(r.Context(), roomID) {
		if g.RevokedAtMs != nil {
			continue
		}
		if g.ExpiresAtMs != nil && now >= *g.ExpiresAtMs {
			continue
		}
		provider := providerFromTarget(g.TargetID)
		// Platform agents have their own listing (GET .../platform-agents).
		if provider == "" || g.Source == store.GrantSourcePlatform {
			continue
		}
		models := make([]string, 0, len(g.AllowedModels))
		for _, m := range g.AllowedModels {
			if m.Model != "" {
				models = append(models, m.Model)
			}
		}
		entries = append(entries, sharedAgentEntry{
			TargetID:      g.TargetID,
			OwnerUserID:   g.OwnerUserID,
			Provider:      provider,
			AllowedModels: models,
			HasQuota:      g.RunQuota != nil,
		})
	}
	writeJSON(w, http.StatusOK, entries)
}
