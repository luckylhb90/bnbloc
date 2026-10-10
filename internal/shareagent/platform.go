package shareagent

import (
	"context"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Platform agents (docs/prd/2026-10-10-zero-install-onboarding.md §2,
// docs/prd/2026-10-10-platform-agents-v2-onboarding.md §3–4): a room holds a
// platform grant (store.GrantSourcePlatform) per added platform agent and per
// host serving it, targeting "shared-agent:<core>:<provider>" of that host.
// Whether such a grant is usable, and whether a chat to it may be sent, is
// decided here, once, for every service that asks (shareagent's room listing
// and reserve path, activity's room agent catalogs, realtime's chat guard).

// PlatformAgentOfTarget parses a platform grant target
// "shared-agent:<core>:<provider>" (split on the first colon: a core name
// never holds one; the provider is the remainder).
func PlatformAgentOfTarget(targetID string) (wire.PlatformAgentRef, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(targetID), "shared-agent:")
	if !ok {
		return wire.PlatformAgentRef{}, false
	}
	core, provider, ok := strings.Cut(strings.TrimSpace(rest), ":")
	if !ok || core == "" || provider == "" {
		return wire.PlatformAgentRef{}, false
	}
	return wire.PlatformAgentRef{Core: core, Provider: provider}, true
}

// PlatformTarget is the grant target of a platform agent (the inverse of
// PlatformAgentOfTarget; the bridge's borrowTarget shape).
func PlatformTarget(ref wire.PlatformAgentRef) string {
	return "shared-agent:" + ref.Core + ":" + ref.Provider
}

// RoomGrantLister lists a room's grants in the ctx tenant (store.GrantStore).
type RoomGrantLister interface {
	RoomGrants(ctx context.Context, roomID string) []store.Grant
}

// RoomPlatformGrant is one platform grant of a room and whether its agent may
// be used there now.
type RoomPlatformGrant struct {
	Grant store.Grant
	// Agent is the grant's (core, provider); zero when the target is malformed.
	Agent wire.PlatformAgentRef
	// Live: not revoked and not expired (removal revokes).
	Live bool
	// Available: the grant is live, well-formed, and the policy still
	// publishes its host's agent.
	Available bool
}

// RoomPlatformGrants lists every platform grant of the room in the ctx tenant
// — revoked and expired ones included, so a caller can tell a host that once
// served the room from one that never did — each with its availability under
// policy at nowMs.
func RoomPlatformGrants(ctx context.Context, grants RoomGrantLister, policy wire.PlatformAgentsPolicy, roomID string, nowMs int64) []RoomPlatformGrant {
	var out []RoomPlatformGrant
	for _, g := range grants.RoomGrants(ctx, roomID) {
		if g.Source != store.GrantSourcePlatform {
			continue
		}
		live := g.RevokedAtMs == nil && (g.ExpiresAtMs == nil || nowMs < *g.ExpiresAtMs)
		ref, valid := PlatformAgentOfTarget(g.TargetID)
		out = append(out, RoomPlatformGrant{
			Grant:     g,
			Agent:     ref,
			Live:      live,
			Available: live && valid && policy.Publishes(g.OwnerUserID, ref.Core, ref.Provider),
		})
	}
	return out
}

// AgentAdvertisements is the bridge advertisement authority (the source of
// GET /v1/my/agents): an agent is online while its owner's live bridge
// advertises it. store.BridgeJobStore satisfies it.
type AgentAdvertisements interface {
	ListBridgeAgentsForOwner(ctx context.Context, ownerUserID string) []store.BridgeAgent
}

// OnlineAgents indexes an owner's live advertised agents by (core, provider)
// to the advertised target id (disabled ones are not online).
func OnlineAgents(advertised []store.BridgeAgent) map[wire.PlatformAgentRef]string {
	out := map[wire.PlatformAgentRef]string{}
	for _, a := range advertised {
		if !a.Disabled {
			out[wire.PlatformAgentRef{Core: a.Core, Provider: a.Provider}] = a.TargetID
		}
	}
	return out
}

// PlatformDrive is the platform-agent reading of a chat addressed to owner's
// (core, provider) in a room by author.
type PlatformDrive struct {
	// Platform: the room holds an available platform grant of owner's agent
	// and author is someone else (a member driving their own agent, or an
	// ordinary borrow, is never a platform drive).
	Platform bool
	// Online: owner's live bridge advertises the agent.
	Online bool
}

// ReadPlatformDrive classifies a chat recipient. The policy, grants and
// advertisements are all shared-store reads, so every caller decides the
// same way.
func ReadPlatformDrive(ctx context.Context, grants RoomGrantLister, policy wire.PlatformAgentsPolicy, ads AgentAdvertisements,
	roomID, author, owner string, ref wire.PlatformAgentRef, nowMs int64) PlatformDrive {
	if owner == "" || strings.EqualFold(author, owner) || ref.Core == "" || ref.Provider == "" {
		return PlatformDrive{}
	}
	platform := false
	for _, pg := range RoomPlatformGrants(ctx, grants, policy, roomID, nowMs) {
		if pg.Available && pg.Grant.OwnerUserID == owner && pg.Agent == ref {
			platform = true
			break
		}
	}
	if !platform {
		return PlatformDrive{}
	}
	_, online := OnlineAgents(ads.ListBridgeAgentsForOwner(ctx, owner))[ref]
	return PlatformDrive{Platform: true, Online: online}
}
