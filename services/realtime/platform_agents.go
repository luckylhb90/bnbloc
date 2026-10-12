package realtimehttp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Platform agents' offline guard
// (docs/prd/2026-10-10-platform-agents-v2-onboarding.md §3). A chat addressed
// to a platform agent (someone else's agent the room holds an available
// platform grant of) whose host's bridge does not advertise it right now is
// refused before it is published, so no client can leave a dead job behind
// that would only time out. PostChat is the one chat publish path (typed and
// scheduled messages), so this is the single point the job could be created.
// Own agents and ordinary borrows are never refused here.

// PlatformAgentGuard holds the shared-store reads of the guard: the room's
// grants, the platform-agent policy, and the hosts' live advertisements.
type PlatformAgentGuard struct {
	Grants   shareagent.RoomGrantLister
	Settings store.PlatformSettingsStore
	Bridges  shareagent.AgentAdvertisements
}

// platformAgentOfflineMessage is the member-facing sentence of the 409.
const platformAgentOfflineMessage = "This platform agent is offline — its host's computer is not connected. Try again later."

// errPlatformAgentOffline refuses a chat to an offline platform agent (409).
type errPlatformAgentOffline struct {
	host string
	ref  wire.PlatformAgentRef
}

func (e errPlatformAgentOffline) Error() string {
	return "platform agent " + e.ref.Core + "/" + e.ref.Provider + " of " + e.host + " is offline"
}

// checkPlatformAgentOnline returns errPlatformAgentOffline for a chat by
// author to an offline platform agent. Fail-open by design: this is a
// courtesy pre-check that spares a member a job no host will take, not an
// access decision. An unreadable policy, grant or advertisement does not
// refuse the chat (that would block chat on a read blip), because the
// host's reserve is the authority and fails closed anyway.
func (s *Service) checkPlatformAgentOnline(ctx context.Context, roomID, author string, rc *ChatRecipient) error {
	g := s.PlatformAgents
	if g == nil || rc == nil {
		return nil
	}
	policy, err := store.LoadPlatformAgents(ctx, g.Settings)
	if err != nil {
		slog.Warn("bnbloc realtime: platform agents policy unreadable; skipping the offline guard",
			"event", "bnbloc.realtime.platform_policy_unreadable", "room", roomID, "error", err)
		return nil
	}
	owner := strings.TrimSpace(rc.OwnerID)
	ref := wire.PlatformAgentRef{Core: strings.TrimSpace(rc.Core), Provider: strings.TrimSpace(rc.Provider)}
	d, err := shareagent.ReadPlatformDrive(ctx, g.Grants, policy, g.Bridges, roomID, author, owner, ref, time.Now().UnixMilli())
	if err != nil {
		slog.Warn("bnbloc realtime: platform drive unreadable; skipping the offline guard",
			"event", "bnbloc.realtime.platform_policy_unreadable", "room", roomID, "error", err)
		return nil
	}
	if d.Platform && !d.Online {
		return errPlatformAgentOffline{host: owner, ref: ref}
	}
	return nil
}

// writePlatformAgentOffline answers 409 {error: platform_agent_offline, ...}.
func writePlatformAgentOffline(w http.ResponseWriter, e errPlatformAgentOffline) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(wire.PlatformAgentOfflineReply{
		Error: wire.ErrPlatformAgentOffline, Message: platformAgentOfflineMessage,
		HostUserID: e.host, Core: e.ref.Core, Provider: e.ref.Provider,
	})
}
