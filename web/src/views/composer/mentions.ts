import { agentDisplayLabel, defaultDisplayName } from "../../lib/displayNames.ts";
import { agentHandle } from "./types.ts";

// The @-mention list shows display names but inserts the canonical handle
// (PRD 2026-10-10 §1): a person's user id, or an agent's
// "[core/]owner/provider". The id stays the identity for recipients.

type AgentRef = { readonly ownerId: string; readonly provider: string; readonly core?: string };

// mentionLabel is what the picker shows for one mention candidate.
export function mentionLabel(
  candidate: string,
  agents: readonly AgentRef[],
  nameOf: (id: string) => string = defaultDisplayName,
): string {
  if (!candidate.includes("/")) return nameOf(candidate);
  const agent = agents.find((a) => agentHandle(a) === candidate);
  if (agent) return agentDisplayLabel(agent, nameOf);
  const parts = candidate.split("/");
  const provider = parts.pop() ?? "";
  const owner = parts.pop() ?? "";
  return agentDisplayLabel({ ownerId: owner, provider }, nameOf);
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
