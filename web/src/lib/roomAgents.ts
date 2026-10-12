// Room agent identity helpers shared by the composer, the borrow dialog and
// the send path. An agent is identified by (owner, core, provider): the same
// harness under two cores, or the same harness of two members, are different
// agents and must never collapse into one row or one authorization.
import type { RoomAgent } from "../views/composer/types.ts";
import { sameUser } from "./format.ts";

// An agent a member's bridge advertises as reachable in the room.
export interface RosterAgent {
  readonly ownerUserId: string;
  readonly core?: string;
  readonly provider: string;
  // Borrowed drives of this agent run in its owner's agent sandbox.
  readonly sandboxed?: boolean;
}

// A live borrow authorization listed by the shareagent authority. Its
// `provider` is the authorized identity: "<core>:<provider>" for a
// core-qualified grant, or the bare provider for a coreless one.
export interface SharedAgent {
  readonly ownerUserId: string;
  readonly provider: string;
  // The models the owner authorized for this borrow; empty = unrestricted.
  readonly allowedModels?: readonly string[];
}


// borrowTarget is the shared-agent identity the authority keys grants by,
// matching the bridge's borrowTarget (services/bridge): a colon separates core
// and provider so the id stays one URL path segment; a coreless agent uses the
// bare provider.
export function borrowTarget(a: { readonly core?: string; readonly provider: string }): string {
  return `shared-agent:${borrowKey(a)}`;
}

// borrowKey is the grant suffix (<core>:<provider>, or the bare provider),
// which is also what the authority echoes back as a usage row's provider.
export function borrowKey(a: { readonly core?: string; readonly provider: string }): string {
  return a.core ? `${a.core}:${a.provider}` : a.provider;
}

// agentIdentity is the stable (owner, core, provider) key of a room agent.
export function agentIdentity(a: { readonly ownerId: string; readonly core?: string; readonly provider: string }): string {
  return `${a.ownerId}/${a.core ?? ""}/${a.provider}`;
}

// mergeRoomAgents builds the room's selectable agents: the live roster (the
// source of truth for what can be driven), plus authorized-but-not-advertised
// agents of other members so a browser user can still discover them. A grant
// whose identity matches a roster agent of the same owner is that agent, not
// a second row.
export function mergeRoomAgents(
  roster: readonly RosterAgent[],
  shared: readonly SharedAgent[],
  subject: string | null,
): RoomAgent[] {
  const agents: RoomAgent[] = roster.map((a) => ({
    ownerId: a.ownerUserId,
    provider: a.provider,
    core: a.core || undefined,
    self: sameUser(a.ownerUserId, subject),
    ...(a.sandboxed ? { sandboxed: true } : {}),
  }));
  const grants = new Set(agents.map((a) => `${a.ownerId}|${borrowKey(a)}`));
  for (const s of shared) {
    if (sameUser(s.ownerUserId, subject)) continue;
    const key = `${s.ownerUserId}|${s.provider}`;
    if (grants.has(key)) continue;
    grants.add(key);
    agents.push({ ownerId: s.ownerUserId, provider: s.provider, self: false });
  }
  return agents;
}
