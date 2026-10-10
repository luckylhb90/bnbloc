// Wire shapes and pure helpers for agent run records (R10), mirroring
// internal/wire/agent_runs.go. A run is one agent job as a record: request,
// addressed agent, per-phase timestamps, outcome, reply preview and reported
// usage. Optional fields are absent when nothing backs them (an absent cost
// means "not reported", never zero).

import { agentDisplayLabel, defaultDisplayName } from "./displayNames.ts";

export type RunScope = "room" | "self" | "tenant";

export interface AgentRunRequest {
  messageSeq: number;
  clientMsgId?: string;
  text?: string;
  attachments?: number;
}

export interface AgentRunAgent {
  owner: string;
  core: string;
  provider: string;
  targetId?: string;
  model?: string;
  reasoningEffort?: string;
}

export interface AgentRunPhases {
  queuedAtMs: number;
  claimedAtMs?: number;
  startedAtMs?: number;
  firstReplyAtMs?: number;
  finishedAtMs?: number;
  updatedAtMs?: number;
}

export interface AgentRunReply {
  messageId?: string;
  roomSeq?: number;
  text?: string;
  atMs?: number;
  count?: number;
}

export interface AgentRunUsage {
  model?: string;
  inputTokens?: number;
  outputTokens?: number;
  costUsd?: number;
}

export interface AgentRun {
  jobId: string;
  roomId: string;
  roomName?: string;
  channelId?: string;
  requester: string;
  borrowed?: boolean;
  status: string;
  failureReason?: string;
  error?: string;
  warning?: string;
  attempt?: number;
  request: AgentRunRequest;
  agent: AgentRunAgent;
  phases: AgentRunPhases;
  reply?: AgentRunReply;
  usage?: AgentRunUsage;
  redacted?: boolean;
  diagnostics?: Record<string, unknown>;
}

export interface AgentRunsReply {
  scope: RunScope;
  runs: AgentRun[];
  nextBefore?: string;
}

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

const LIVE = new Set(["queued", "claimed", "running", "reflowing"]);

// isLiveRun reports a run that has not reached a terminal status.
export function isLiveRun(r: Pick<AgentRun, "status">): boolean {
  return LIVE.has(r.status);
}

// formatDuration renders a millisecond span compactly: 850ms, 12.4s, 3m 05s,
// 1h 02m.
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(Math.floor(s % 60)).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
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
    { id: "finished", label: finishLabel, atMs: isLiveRun(r) ? undefined : p.finishedAtMs },
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
  const end = isLiveRun(r) ? now : r.phases.finishedAtMs;
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
