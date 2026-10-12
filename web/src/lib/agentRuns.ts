// Pure helpers for agent run records (R10); the shapes are
// internal/wire/agent_runs.go's, generated into wire.gen.ts. A run is one agent job as a record: request,
// addressed agent, per-phase timestamps, outcome, reply preview and reported
// usage. Optional fields are absent when nothing backs them (an absent cost
// means "not reported", never zero).

import { agentDisplayLabel, defaultDisplayName } from "./displayNames.ts";
import { AgentJobStatusValues, AgentJobTerminalStatuses } from "./wire.gen.ts";

export type {
  AgentRun,
  AgentRunAgent,
  AgentRunPhases,
  AgentRunReply,
  AgentRunRequest,
  AgentRunsReply,
  AgentRunUsage,
  RunScope,
} from "./wire.gen.ts";
import type { AgentRun, AgentRunAgent, RunScope } from "./wire.gen.ts";

// Status filter choices the run views offer ("" = every status).
export type RunStatusFilter = "" | "active" | "failed" | "replied" | "cancelled";

// statusParam maps a filter choice to the server's status list: "failed"
// covers both failure statuses (failed and timed out).
export function statusParam(f: RunStatusFilter): string {
  switch (f) {
    case "failed":
      return "failed,timed_out";
    case "":
      return "";
    default:
      return f;
  }
}

export interface RunListQuery {
  status?: RunStatusFilter;
  member?: string;
  owner?: string;
  core?: string;
  provider?: string;
  fromMs?: number;
  toMs?: number;
  before?: string;
  limit?: number;
}

// runsPath builds a run listing path for one scope (roomId for "room").
export function runsPath(scope: RunScope, q: RunListQuery = {}, roomId = ""): string {
  const base =
    scope === "room"
      ? `/v1/rooms/${encodeURIComponent(roomId)}/jobs`
      : scope === "self"
        ? "/v1/my/jobs"
        : "/v1/tenant/jobs";
  const p = new URLSearchParams();
  const status = statusParam(q.status ?? "");
  if (status) p.set("status", status);
  if (scope === "tenant" && q.member?.trim()) p.set("member", q.member.trim());
  if (q.owner) p.set("owner", q.owner);
  if (q.core) p.set("core", q.core);
  if (q.provider) p.set("provider", q.provider);
  if (q.fromMs) p.set("from", String(q.fromMs));
  if (q.toMs) p.set("to", String(q.toMs));
  if (q.before) p.set("before", q.before);
  if (q.limit) p.set("limit", String(q.limit));
  const qs = p.toString();
  return qs ? `${base}?${qs}` : base;
}

// isFailedRun reports a run that ended in failure (highlighted in lists).
export function isFailedRun(r: Pick<AgentRun, "status">): boolean {
  return r.status === "failed" || r.status === "timed_out";
}

// isLiveJob reports a job (or run) that has not reached a terminal status:
// store.AgentJobStatus.Terminal is the one rule (AgentJobTerminalStatuses).
export function isLiveJob(r: { readonly status: string }): boolean {
  return (AgentJobStatusValues as readonly string[]).includes(r.status) && !(AgentJobTerminalStatuses as readonly string[]).includes(r.status);
}


export interface RunPhaseStep {
  readonly id: "queued" | "claimed" | "started" | "firstReply" | "finished";
  readonly label: string;
  readonly atMs: number;
  // sincePrevMs is the time since the previous recorded phase.
  readonly sincePrevMs?: number;
}

// runPhaseSteps lists the phases that happened, in order, each with the time
// spent since the previous one. The final phase is labelled by outcome.
export function runPhaseSteps(r: Pick<AgentRun, "phases" | "status">): RunPhaseStep[] {
  const p = r.phases;
  const finishLabel =
    r.status === "replied"
      ? "Replied"
      : r.status === "cancelled"
        ? "Cancelled"
        : r.status === "timed_out"
          ? "Timed out"
          : "Failed";
  const raw: { id: RunPhaseStep["id"]; label: string; atMs?: number }[] = [
    { id: "queued", label: "Queued", atMs: p.queuedAtMs },
    { id: "claimed", label: "Claimed by bridge", atMs: p.claimedAtMs },
    { id: "started", label: "Started", atMs: p.startedAtMs },
    { id: "firstReply", label: "First reply", atMs: p.firstReplyAtMs },
    { id: "finished", label: finishLabel, atMs: isLiveJob(r) ? undefined : p.finishedAtMs },
  ];
  const out: RunPhaseStep[] = [];
  let prev: number | undefined;
  for (const s of raw) {
    if (!s.atMs) continue;
    out.push({ id: s.id, label: s.label, atMs: s.atMs, sincePrevMs: prev === undefined ? undefined : Math.max(0, s.atMs - prev) });
    prev = s.atMs;
  }
  return out;
}

// runDurationMs is the run's total time: queued to finished (or to now while
// it is still live).
export function runDurationMs(r: Pick<AgentRun, "phases" | "status">, now: number = Date.now()): number | undefined {
  const end = isLiveJob(r) ? now : r.phases.finishedAtMs;
  return end ? Math.max(0, end - r.phases.queuedAtMs) : undefined;
}

// runAgentLabel is the addressed agent by its owner's display name
// ("Maya · codex"), with the core appended when present.
export function runAgentLabel(a: AgentRunAgent, nameOf: (id: string) => string = defaultDisplayName): string {
  return agentDisplayLabel({ ownerId: a.owner, provider: a.provider, core: a.core || undefined }, nameOf);
}

// mergeRunPages appends an older page to the loaded runs, dropping any run
// already present (a run can shift pages while new ones arrive).
export function mergeRunPages(loaded: readonly AgentRun[], older: readonly AgentRun[]): AgentRun[] {
  const seen = new Set(loaded.map((r) => r.jobId));
  return [...loaded, ...older.filter((r) => !seen.has(r.jobId))];
}
