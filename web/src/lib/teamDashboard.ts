// Pure helpers for the team agent dashboard (F1) and borrow billing (F3);
// the shapes are internal/wire/team_agents.go's (wire.gen.ts). Optional
// fields are absent when the server has no source for them (a missing cost
// means "not reported", never zero).

import { compactNumber } from "./format.ts";

export type TeamScope = "self" | "tenant";

export type { QuotaRequestStatus, TeamJob, TeamRange, UsageAs, UsageGroupBy } from "./wire.gen.ts";
import type { QuotaRequestStatus, TeamRange, UsageAs, UsageGroupBy } from "./wire.gen.ts";

export type { BorrowUsageReport, CreateQuotaRequestInput, QuotaRequest, TeamAgent, TeamAgentsReply } from "./wire.gen.ts";
import type { QuotaRequest, TeamAgent } from "./wire.gen.ts";



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
    const usageKey = `${a.owner}\u0000${a.provider}`;
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
