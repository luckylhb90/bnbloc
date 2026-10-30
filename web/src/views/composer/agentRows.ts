// Rows of the composer's agent picker. One row per agent identity
// (owner, core, provider): my local target and my own advertised roster agent
// for the same (core, provider) are one row, but another member's agent with
// the same harness is its own row — selecting it borrows THAT member's agent.
import { PLATFORM_AGENTS_GROUP, samePlatformAgent, type PlatformAgentRef, type PlatformChoice } from "../../lib/platformAgents.ts";
import { agentIdentity, coreGroup } from "../../lib/roomAgents.ts";
import type { RoomAgent } from "./types.ts";

export interface PickerTarget {
  readonly id: string;
  readonly core: string;
  readonly provider: string;
}

export interface AgentRow {
  readonly key: string;
  // Grouping label: the core, or "other" for coreless agents.
  readonly group: string;
  readonly core: string;
  readonly provider: string;
  readonly ownerId: string;
  readonly self: boolean;
  // Set when the row drives one of my local targets (desktop shell).
  readonly localTargetId?: string;
  // Set when the row is a room-advertised agent (picked as the recipient).
  readonly agent?: RoomAgent;
  // Set on a "Platform agents" row: picking one not yet in the room adds it
  // first (confirmed).
  readonly platform?: PlatformChoice;
}

// Platform agents form their own last group (and only appear there), whether
// or not they are in the room yet.
export function agentRows(
  localTargets: readonly PickerTarget[],
  roomAgents: readonly RoomAgent[],
  subject: string | null,
  platform: readonly PlatformChoice[] = [],
): { readonly group: string; readonly rows: readonly AgentRow[] }[] {
  const me = subject ?? "";
  // A platform agent's identity on every host it may be routed to.
  const platformKeys = new Set(
    platform.flatMap((c) => [c.agent, ...c.hosts.map((h) => ({ ...c.agent, ownerId: h.hostUserId }))].map(agentIdentity)),
  );
  const byKey = new Map<string, AgentRow>();
  for (const t of localTargets) {
    const key = agentIdentity({ ownerId: me, core: t.core, provider: t.provider });
    if (byKey.has(key)) continue;
    byKey.set(key, {
      key,
      group: coreGroup(t),
      core: t.core,
      provider: t.provider,
      ownerId: me,
      self: true,
      localTargetId: t.id,
    });
  }
  for (const a of roomAgents) {
    const key = agentIdentity(a);
    if (platformKeys.has(key) || (a.platform && platform.some((c) => samePlatformAgent(platformRef(c.agent), platformRef(a))))) continue;
    const existing = byKey.get(key);
    if (existing) {
      if (!existing.agent) byKey.set(key, { ...existing, agent: a });
      continue;
    }
    byKey.set(key, {
      key,
      group: coreGroup(a),
      core: a.core ?? "",
      provider: a.provider,
      ownerId: a.ownerId,
      self: a.self,
      agent: a,
    });
  }
  const groups: { group: string; rows: AgentRow[] }[] = [];
  for (const row of rankRows([...byKey.values()])) {
    let g = groups.find((x) => x.group === row.group);
    if (!g) {
      g = { group: row.group, rows: [] };
      groups.push(g);
    }
    g.rows.push(row);
  }
  if (platform.length > 0) {
    groups.push({
      group: PLATFORM_AGENTS_GROUP,
      rows: platform.map((c) => ({
        key: platformRowKey(c.agent),
        group: PLATFORM_AGENTS_GROUP,
        core: c.agent.core ?? "",
        provider: c.agent.provider,
        ownerId: c.agent.ownerId,
        self: false,
        agent: c.agent,
        platform: c,
      })),
    });
  }
  return groups;
}

// An agent that is also offered through a wrapper (a provider named
// "<prefix>-<name>" beside "<name>" in the same core) is the original: it needs
// its own sign-in or key, the wrapped one does not, so it lists after the
// wrapped one. The order is otherwise the order the rows arrived in. One rule
// for every agent: no name is known here.
export function rankRows(rows: readonly AgentRow[]): AgentRow[] {
  const isOriginal = (r: AgentRow) =>
    rows.some((o) => o !== r && o.core === r.core && o.ownerId === r.ownerId && o.provider.endsWith(`-${r.provider}`));
  const original = new Map(rows.map((r) => [r, isOriginal(r)]));
  return [...rows].sort((a, b) => Number(original.get(a)) - Number(original.get(b)));
}

function platformRef(a: { readonly core?: string; readonly provider: string }): PlatformAgentRef {
  return { core: a.core ?? "", provider: a.provider };
}

// A platform row is one (core, provider) whichever host serves it.
function platformRowKey(a: { readonly core?: string; readonly provider: string }): string {
  return `platform:${agentIdentity({ ownerId: "", core: a.core || undefined, provider: a.provider })}`;
}

// rowIsActive reports whether a row is the current selection: an explicit
// recipient matches by identity (a platform agent by (core, provider), on
// any host); with no recipient, my selected local target.
export function rowIsActive(row: AgentRow, recipient: RoomAgent | null, selectedTargetId: string): boolean {
  if (recipient?.platform && row.platform) return platformRowKey(recipient) === row.key;
  if (recipient) return agentIdentity(recipient) === row.key;
  return !!row.localTargetId && row.localTargetId === selectedTargetId;
}
