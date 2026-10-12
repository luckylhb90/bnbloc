// Room agent identity helpers shared by the composer, the borrow dialog and
// the send path. An agent is identified by (owner, core, provider): the same
// harness under two cores, or the same harness of two members, are different
// agents and must never collapse into one row or one authorization.
import type { RoomAgent } from "../views/composer/types.ts";
import { sameUser } from "./format.ts";

export type { RosterAgent } from "./wire.gen.ts";
import type { RosterAgent } from "./wire.gen.ts";

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

// AgentRef names one member's agent. Owner, core and provider compare
// exactly (subjects are case-sensitive; core and provider names are ids).
export interface AgentRef {
  readonly ownerId: string;
  readonly core?: string;
  readonly provider: string;
}

// agentIdentity is the stable (owner, core, provider) key of an agent: the
// one key for maps, React keys and change detection.
export function agentIdentity(a: AgentRef): string {
  return `${a.ownerId}/${a.core ?? ""}/${a.provider}`;
}

// sameAgent reports whether a and b are the same agent (owner, core and
// provider alike).
export function sameAgent(a: AgentRef, b: AgentRef): boolean {
  return agentIdentity(a) === agentIdentity(b);
}

// agentHandle is an agent's text handle: "core/owner/provider" when it has a
// core (two cores exposing the same owner/provider stay distinct in
// mentions, templates and recipient resolution), "owner/provider" without
// one, the bare provider when no owner is known. parseAgentHandle reads it
// back.
export function agentHandle(a: AgentRef): string {
  if (!a.ownerId) return a.provider;
  return a.core ? `${a.core}/${a.ownerId}/${a.provider}` : `${a.ownerId}/${a.provider}`;
}

// parseAgentHandle reads an agentHandle ("owner/provider" or
// "core/owner/provider"); anything else (a bare user id, an empty part) is
// not an agent handle.
export function parseAgentHandle(handle: string): AgentRef | null {
  const parts = handle.trim().split("/").map((p) => p.trim());
  if (parts.some((p) => !p)) return null;
  if (parts.length === 2) return { ownerId: parts[0], provider: parts[1] };
  if (parts.length === 3) return { core: parts[0], ownerId: parts[1], provider: parts[2] };
  return null;
}

// NO_CORE_GROUP is the group of an agent without a core (a borrowed or
// legacy target).
export const NO_CORE_GROUP = "other";

// coreGroup is the group an agent lists under in a picker: its core.
export function coreGroup(a: { readonly core?: string }): string {
  return a.core || NO_CORE_GROUP;
}

// groupByCore buckets items by coreGroup, in first-seen order (the agent
// picker, the run dialog and the borrow dialog group alike).
export function groupByCore<T extends { readonly core?: string }>(items: readonly T[]): { readonly core: string; readonly items: T[] }[] {
  const groups: { core: string; items: T[] }[] = [];
  for (const it of items) {
    const core = coreGroup(it);
    let g = groups.find((x) => x.core === core);
    if (!g) {
      g = { core, items: [] };
      groups.push(g);
    }
    g.items.push(it);
  }
  return groups;
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
