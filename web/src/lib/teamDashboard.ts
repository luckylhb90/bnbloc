// Wire shapes and pure helpers for the team agent dashboard (F1) and borrow
// billing (F3), mirroring internal/wire/team_agents.go. Optional fields are
// absent when the server has no source for them (a missing cost means "not
// reported", never zero).

import { compactNumber } from "./format.ts";

export type TeamRange = "today" | "7d";
export type TeamScope = "self" | "tenant";

export interface TeamJob {
  jobId: string;
  roomId: string;
  roomName?: string;
  channelId?: string;
  requester: string;
  status: string;
  failureReason?: string;
  createdAtMs: number;
  startedAtMs?: number;
  finishedAtMs?: number;
}

export interface TeamAgentUsage {
  reportedTurns: number;
  inputTokens: number;
  outputTokens: number;
  costUsd?: number;
  shared?: boolean;
}

export interface TeamAgent {
  owner: string;
  core: string;
  provider: string;
  targetId?: string;
  online: boolean;
  lastSeenAtMs?: number;
  currentJob?: TeamJob;
  lastJob?: TeamJob;
  turns: number;
  failedTurns: number;
  borrowedRuns: number;
  usage?: TeamAgentUsage;
}

export interface TeamAgentsReply {
  scope: TeamScope;
  range: TeamRange;
  sinceMs: number;
  agents: TeamAgent[];
}

export type UsageGroupBy = "day" | "month";
export type UsageAs = "owner" | "borrower" | "tenant";

export interface BorrowUsageRow {
  period: string;
  owner: string;
  borrower: string;
  provider: string;
  roomId: string;
  runs: number;
  cost?: number;
  inputTokens?: number;
  outputTokens?: number;
}

export interface BorrowUsageReport {
  as: UsageAs;
  groupBy: UsageGroupBy;
  fromMs: number;
  toMs: number;
  rows: BorrowUsageRow[];
  totalRuns: number;
  totalCost?: number;
  totalInputTokens?: number;
  totalOutputTokens?: number;
  truncated?: boolean;
}

export type QuotaRequestStatus = "pending" | "approved" | "declined";

export interface QuotaRequest {
  requestId: string;
  roomId: string;
  owner: string;
  requester: string;
  provider: string;
  requestedRuns?: number;
  note?: string;
  status: QuotaRequestStatus;
  grantedRuns?: number;
  createdAtMs: number;
  decidedAtMs?: number;
}

export interface CreateQuotaRequestInput {
  roomId: string;
  owner: string;
  provider: string;
  requestedRuns?: number;
  note: string;
}

export interface UsageReportQuery {
  as: UsageAs;
  groupBy: UsageGroupBy;
  // Calendar month "YYYY-MM" the report covers (local time).
  month: string;
}

// Seconds-free refresh cadence of the dashboard while visible.
export const TEAM_REFRESH_MS = 30_000;

// teamAgentsPath builds the dashboard request: the tenant-wide view lives under
// /v1/tenant (org admins), the own-agents view under /v1/team. tzOffsetMin lets
// the server start "today" at the viewer's local midnight.
export function teamAgentsPath(scope: TeamScope, range: TeamRange, tzOffsetMin: number): string {
  const base = scope === "tenant" ? "/v1/tenant/team/agents" : "/v1/team/agents";
  return `${base}?range=${range}&tzOffsetMin=${tzOffsetMin}`;
}

// currentMonth is "YYYY-MM" for a date in local time.
export function currentMonth(d: Date = new Date()): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
}

// monthBounds returns the [from, to) unix-ms bounds of a local "YYYY-MM"
// month, or null when the value is not a month.
export function monthBounds(month: string): { fromMs: number; toMs: number } | null {
  const m = /^(\d{4})-(\d{2})$/.exec(month);
  if (!m) return null;
  const year = Number(m[1]);
  const mon = Number(m[2]);
  if (mon < 1 || mon > 12) return null;
  return { fromMs: new Date(year, mon - 1, 1).getTime(), toMs: new Date(year, mon, 1).getTime() };
}

// usageReportPath builds the usage-report request for a month. The tenant-wide
// report is an org-admin path; owner/borrower are the caller's own sides.
export function usageReportPath(q: UsageReportQuery, tzOffsetMin: number, format?: "csv"): string | null {
  const b = monthBounds(q.month);
  if (!b) return null;
  const params = new URLSearchParams({
    groupBy: q.groupBy,
    from: String(b.fromMs),
    to: String(b.toMs),
    tzOffsetMin: String(tzOffsetMin),
  });
  if (q.as !== "tenant") params.set("as", q.as);
  if (format) params.set("format", format);
  const base = q.as === "tenant" ? "/v1/tenant/borrow/usage-report" : "/v1/borrow/usage-report";
  return `${base}?${params}`;
}

// formatCost renders a reported cost; undefined stays "—" (not reported).
export function formatCost(cost: number | undefined): string {
  if (cost === undefined) return "—";
  return cost < 0.01 && cost > 0 ? `$${cost.toFixed(4)}` : `$${cost.toFixed(2)}`;
}

// formatTokens renders reported token usage as "1.2k in / 340 out"; "—" when
// neither count was reported (an absent count is never shown as 0).
export function formatTokens(input: number | undefined, output: number | undefined): string {
  if (input === undefined && output === undefined) return "—";
  const n = (v: number | undefined) => (v === undefined ? "—" : compactNumber(v));
  return `${n(input)} in / ${n(output)} out`;
}


// summarizeTeam totals the dashboard rows for the header strip.
export function summarizeTeam(agents: readonly TeamAgent[]): {
  online: number;
  busy: number;
  turns: number;
  borrowedRuns: number;
  costUsd?: number;
} {
  let online = 0;
  let busy = 0;
  let turns = 0;
  let borrowedRuns = 0;
  let costUsd: number | undefined;
  // A Shared usage total appears on several rows; count it once per owner+provider.
  const countedUsage = new Set<string>();
  for (const a of agents) {
    if (a.online) online++;
    if (a.currentJob) busy++;
    turns += a.turns;
    borrowedRuns += a.borrowedRuns;
    const usageKey = `${a.owner.toLowerCase()}\u0000${a.provider}`;
    if (a.usage?.costUsd !== undefined && !countedUsage.has(usageKey)) {
      countedUsage.add(usageKey);
      costUsd = (costUsd ?? 0) + a.usage.costUsd;
    }
  }
  return { online, busy, turns, borrowedRuns, costUsd };
}

// sortQuotaRequests puts pending requests first, newest first within a status.
export function sortQuotaRequests(xs: readonly QuotaRequest[]): QuotaRequest[] {
  const rank = (s: QuotaRequestStatus) => (s === "pending" ? 0 : 1);
  return [...xs].sort((a, b) => rank(a.status) - rank(b.status) || b.createdAtMs - a.createdAtMs);
}
