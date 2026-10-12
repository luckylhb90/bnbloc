// Expert teams (docs/prd/2026-10-12-experts-and-teams.md §8): an expert with
// a roster. The shapes are internal/wire/teams.go's (wire.gen.ts); the helpers are pure
// (roster wording, the plan request, the estimate sentence) so they are
// unit-tested.
import { formatUsd } from "./format.ts";

export type { AgentCostSample, ContentTeam, ContentTeamExpert, ContentTeamMember, PlanAgent, TeamCostEstimate, TeamPlanEntry, TeamPlanRequest } from "./wire.gen.ts";
import type { ContentTeam, TeamCostEstimate, TeamPlanEntry, TeamPlanRequest } from "./wire.gen.ts";

export type { TeamPlanReply as TeamPlan } from "./wire.gen.ts";
import type { TeamPlanReply as TeamPlan } from "./wire.gen.ts";

// A role's default agent, as the team block names it.
export type DefaultAgentKind = "requester" | "platform" | "member";

export interface DefaultAgent {
  readonly kind: DefaultAgentKind;
  readonly owner?: string;
  readonly provider?: string;
}

export function parseDefaultAgent(s: string): DefaultAgent | null {
  const v = s.trim();
  if (v === "requester" || v === "platform") return { kind: v };
  if (!v.startsWith("member:")) return null;
  const [owner, provider, extra] = v.slice("member:".length).split("/");
  if (!owner || /\s/.test(owner) || extra !== undefined || provider === "" || /\s/.test(provider ?? "")) return null;
  return provider ? { kind: "member", owner, provider } : { kind: "member", owner };
}

export function formatDefaultAgent(d: DefaultAgent): string {
  if (d.kind !== "member") return d.kind;
  return `member:${d.owner}${d.provider ? `/${d.provider}` : ""}`;
}

// defaultAgentLabel is the roster's "runs on" wording. nameOf turns a member
// id into their display name.
export function defaultAgentLabel(spec: string, nameOf: (id: string) => string = (id) => id): string {
  const d = parseDefaultAgent(spec);
  if (!d) return spec;
  if (d.kind === "requester") return "your own agent";
  if (d.kind === "platform") return "a platform agent";
  return d.provider ? `${nameOf(d.owner!)}'s ${d.provider}` : `${nameOf(d.owner!)}'s agent`;
}

export function planRequest(team: ContentTeam, lead: { ownerId: string; core?: string; provider: string }): TeamPlanRequest {
  return {
    lead: { ownerId: lead.ownerId, provider: lead.provider, ...(lead.core ? { core: lead.core } : {}) },
    members: team.members.map((m) => ({ role: m.role, defaultAgent: m.defaultAgent })),
  };
}

// estimateText is the honest pre-flight sentence: the sum and how many runs
// it rests on, the seats without data named, never a guess.
export function estimateText(e: TeamCostEstimate): string {
  const unknown = e.unknown ?? [];
  if (e.costUsd == null) return "Cost unknown: none of these agents reported a cost on recent runs.";
  const base = `About ${formatUsd(e.costUsd)} per request, based on ${e.runs === 1 ? "the last run" : `the last ${e.runs} runs`}`;
  return unknown.length > 0 ? `${base}; no cost data for ${unknown.join(", ")}.` : `${base}.`;
}

// seatLine is one roster row of a plan: "reviewer → bob/claude" or why the
// role has no agent.
export function seatLine(e: TeamPlanEntry): string {
  const who = e.agent ? e.agent.handle || `${e.agent.ownerId}/${e.agent.provider}` : `unavailable (${e.reason || "no agent"})`;
  return `${e.role ?? "lead"} → ${who}`;
}

// leadLine names the agent that leads the plan: "Lead: jack/codex", and
// for a platform agent the host it runs on ("Lead: codex, a platform agent
// on host-a's machine"), so the member confirms the host the send uses.
export function leadLine(plan: Pick<TeamPlan, "lead">, nameOf: (id: string) => string = (id) => id): string {
  const a = plan.lead.agent;
  if (!a) return "Lead: unavailable";
  const who = a.platform ? `${a.provider}, a platform agent on ${nameOf(a.ownerId)}'s machine` : a.handle || `${a.ownerId}/${a.provider}`;
  return plan.lead.online ? `Lead: ${who}` : `Lead: ${who} (${plan.lead.reason || "not online"})`;
}

// unstaffedRoles names the roles nobody can take now.
export function unstaffedRoles(plan: TeamPlan): string[] {
  return plan.roles.filter((r) => !r.agent).map((r) => r.role ?? "");
}

// teamConfirmKey identifies what a member confirmed: the team (and
// version) on one lead agent. Picking another team or agent asks again.
export function teamConfirmKey(teamId: string, version: string, lead: { ownerId: string; core?: string; provider: string } | null): string {
  return lead ? `${teamId}@${version}:${lead.ownerId}/${lead.core ?? ""}/${lead.provider}` : "";
}
