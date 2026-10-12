package shareagenthttp

import (
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
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
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := s.requireMember(w, r, roomID, callerSubject(r), membership.AnyMember); !ok {
		return
	}
	now := s.Now()
	entries := []sharedAgentEntry{}
	for _, g := range s.Grants.RoomGrants(r.Context(), roomID) {
		if !g.Live(now) {
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
	httpx.WriteJSON(w, http.StatusOK, entries)
}
