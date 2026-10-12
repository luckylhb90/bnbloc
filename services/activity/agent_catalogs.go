package activity

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room agent catalogs (docs/bugs/2026-10-10-composer-model-picker-own-catalog-only.md).
// The composer's model picker needs the catalog of the agent a member picks,
// which is usually not the member's own: a borrowed agent or a platform agent.
// The catalogs are the bridge advertisements this service ingests with each
// heartbeat; this read projects them per (owner, core, provider) for the
// agents a room member may address in that room.
//
// Visibility is the room's: a human member's live agents are already on the
// room roster for every member, so their catalogs are too. An agent-host
// member (a platform agent's host, docs/prd/2026-10-10-zero-install-onboarding.md)
// is not a participant: it contributes ONLY its agents added to this room as
// platform agents that the policy still publishes (shareagent.RoomPlatformGrants,
// the same rule shareagent's room listing and reserve path apply), never the
// host's other agents. The decision is made here, from the platform grants
// and policy in the shared store, so a client can never widen it.

// handleRoomAgentCatalogs is GET /v1/rooms/{roomId}/agent-catalogs.
func (s *Service) handleRoomAgentCatalogs(w http.ResponseWriter, r *http.Request) {
	caller := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	if s.BridgeJobs == nil || s.Rooms == nil {
		obs.ServerError(w, "room agent catalogs not configured", nil)
		writeErr(w, http.StatusNotImplemented, "room agent catalogs not configured")
		return
	}
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	agents := []wire.RoomAgentCatalog{}
	for _, a := range s.roomAddressableAgents(r, roomID) {
		agents = append(agents, a.RoomAgentCatalog)
	}
	writeJSON(w, http.StatusOK, wire.RoomAgentCatalogsReply{Agents: agents})
}

// addressableAgent is one agent a member may address in a room; Platform
// marks a platform agent (an agent-host member's).
type addressableAgent struct {
	wire.RoomAgentCatalog
	Platform bool
}

// roomAddressableAgents lists the agents a room's members may address: its
// human members' live agents and the room's available platform agents
// (never an agent-host member's other agents), sorted by owner, core and
// provider. The room agent catalogs and the team plan both read it.
func (s *Service) roomAddressableAgents(r *http.Request, roomID string) []addressableAgent {
	ctx := r.Context()
	var hosted map[string]map[wire.PlatformAgentRef]bool // host -> added + published agents
	agents := []addressableAgent{}
	for _, m := range s.Rooms.Members(ctx, roomID) {
		var allowed map[wire.PlatformAgentRef]bool
		if m.AgentHost {
			if hosted == nil {
				hosted = s.roomPlatformAgents(r, roomID)
			}
			if allowed = hosted[m.UserID]; len(allowed) == 0 {
				continue
			}
		}
		for _, c := range ownerCatalogs(s.BridgeJobs.ListBridgeAgentsForOwner(ctx, m.UserID), allowed) {
			agents = append(agents, addressableAgent{RoomAgentCatalog: c, Platform: m.AgentHost})
		}
	}
	sort.Slice(agents, func(i, k int) bool {
		a, b := agents[i], agents[k]
		if a.OwnerUserID != b.OwnerUserID {
			return a.OwnerUserID < b.OwnerUserID
		}
		if a.Core != b.Core {
			return a.Core < b.Core
		}
		return a.Provider < b.Provider
	})
	return agents
}

// roomPlatformAgents indexes the room's available platform agents by host.
// It fails closed: no grant store or policy, or an unreadable policy, yields
// none (the hosts' agents are left out; members' own agents still list).
func (s *Service) roomPlatformAgents(r *http.Request, roomID string) map[string]map[wire.PlatformAgentRef]bool {
	out := map[string]map[wire.PlatformAgentRef]bool{}
	if s.Grants == nil || s.Settings == nil {
		return out
	}
	policy, err := store.LoadPlatformAgents(r.Context(), s.Settings)
	if err != nil {
		slog.Warn("bnbloc activity: platform agents policy unreadable; leaving hosts' agents out of the room catalogs",
			"event", "bnbloc.activity.platform_policy_unreadable", "room", roomID, "error", err)
		return out
	}
	for _, pg := range shareagent.RoomPlatformGrants(r.Context(), s.Grants, policy, roomID, s.clock().UnixMilli()) {
		if !pg.Available {
			continue
		}
		host := pg.Grant.OwnerUserID
		if out[host] == nil {
			out[host] = map[wire.PlatformAgentRef]bool{}
		}
		out[host][pg.Agent] = true
	}
	return out
}

// ownerCatalogs projects one owner's live advertised agents, one row per
// (core, provider) — the most recent advertisement wins when several of the
// owner's bridges serve the same agent. A non-nil allowed keeps only those
// agents.
func ownerCatalogs(advertised []store.BridgeAgent, allowed map[wire.PlatformAgentRef]bool) []wire.RoomAgentCatalog {
	latest := map[wire.PlatformAgentRef]store.BridgeAgent{}
	for _, a := range advertised {
		if a.Disabled {
			continue
		}
		ref := wire.PlatformAgentRef{Core: a.Core, Provider: a.Provider}
		if allowed != nil && !allowed[ref] {
			continue
		}
		if cur, ok := latest[ref]; !ok || a.LastSeenAtMs > cur.LastSeenAtMs {
			latest[ref] = a
		}
	}
	out := make([]wire.RoomAgentCatalog, 0, len(latest))
	for ref, a := range latest {
		out = append(out, wire.RoomAgentCatalog{
			OwnerUserID: a.OwnerUserID, Core: ref.Core, Provider: ref.Provider,
			Online: true, ModelOptions: modelOptions(a),
		})
	}
	return out
}

// modelOptions decodes an advertised catalog; absent or malformed is none.
func modelOptions(a store.BridgeAgent) *wire.AgentModelOptions {
	if len(a.ModelOptionsJSON) == 0 {
		return nil
	}
	var o wire.AgentModelOptions
	if err := json.Unmarshal(a.ModelOptionsJSON, &o); err != nil {
		return nil
	}
	return &o
}
