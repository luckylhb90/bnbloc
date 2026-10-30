import { agentDisplayLabel, defaultDisplayName } from "../../lib/displayNames.ts";
import { agentHandle, parseAgentHandle } from "../../lib/roomAgents.ts";

// The @-mention list shows display names but inserts the canonical handle
// (PRD 2026-10-10 §1): a person's user id, or an agent's
// "[core/]owner/provider". The id stays the identity for recipients.

type AgentRef = { readonly ownerId: string; readonly provider: string; readonly core?: string };

// BROADCAST_MENTIONS reach a channel's people rather than one (C5): @here
// those active in it, @channel everyone who may read it; neither is a
// member id (the server reserves both words).
export const BROADCAST_MENTIONS: readonly string[] = ["here", "channel"];

const BROADCAST_LABELS: Record<string, string> = {
  here: "here: people active in this channel",
  channel: "channel: everyone in this channel",
};

// mentionLabel is what the picker shows for one mention candidate.
export function mentionLabel(
  candidate: string,
  agents: readonly AgentRef[],
  nameOf: (id: string) => string = defaultDisplayName,
): string {
  const broadcast = BROADCAST_LABELS[candidate];
  if (broadcast) return broadcast;
  if (!candidate.includes("/")) return nameOf(candidate);
  const agent = agents.find((a) => agentHandle(a) === candidate);
  if (agent) return agentDisplayLabel(agent, nameOf);
  const ref = parseAgentHandle(candidate);
  return ref ? agentDisplayLabel(ref, nameOf) : nameOf(candidate);
}

// mentionMatches filters candidates whose handle OR display label starts with
// the typed query (case-insensitive), at most `limit`.
export function mentionMatches(
  candidates: readonly string[],
  query: string,
  labelOf: (candidate: string) => string,
  limit = 10,
): string[] {
  const q = query.toLowerCase();
  return candidates
    .filter((m) => m.toLowerCase().startsWith(q) || labelOf(m).toLowerCase().startsWith(q))
    .slice(0, limit);
}
