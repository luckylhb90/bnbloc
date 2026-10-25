// The organization board's data: the snapshot contract (version 1, served by the
// pulse service at GET /v1/tenant/pulse/snapshot and in the stream's "snapshot"
// events) and the pure rules the board draws from it. Nothing here is a
// stand-in: a figure the server did not send is absent, never zero.

export type OrgPulseCore = { replies: number; reported: number };

export type OrgPulseSnapshot = {
  v: number;
  atMs: number;
  settlingSinceMs: number;
  // The rollup's last write: how old the figures are (0 = never written).
  dataAsOfMs: number;
  today: {
    replies: number;
    repliesReported: number;
    coverage: number | null;
    repliesByCore: Record<string, OrgPulseCore>;
    tokensIn: number;
    tokensOut: number;
    costUsd: number;
    jobsClaimed: number;
    jobsFinished: number;
    messages: number;
    artifactVersions: number;
    appWrites: number;
  };
  live: {
    agentsWorking: number;
    agentsWaiting: number;
    oldestWaitingSec: number | null;
    peopleOnline: number | null;
    peopleSuppressed: boolean;
  };
  week: { activePeople: number | null; activePeopleSuppressed: boolean };
  // People online per country, only for countries with at least k people (the
  // server withholds the rest and says how many).
  geo: { countries: OrgPulseCountry[]; countriesWithheld: number };
};

export type OrgPulseCountry = { code: string; lat: number; lon: number; people: number };

// Figures older than this are shown as delayed: the consumer flushes every few
// seconds, so a longer gap means no traffic or a stalled pipeline.
export const DELAYED_AFTER_MS = 90_000;

// Deliverables today: new artifact versions, the things members produce. App
// document writes are a separate activity (appWritesToday), not deliverables.
export function deliverablesToday(s: OrgPulseSnapshot): number {
  return s.today.artifactVersions;
}

// App document writes today: writes to interactive apps' data.
export function appWritesToday(s: OrgPulseSnapshot): number {
  return s.today.appWrites;
}

export type Freshness =
  | { state: "none" } // the rollup has never been written
  | { state: "live"; behindSec: number }
  | { state: "delayed"; behindSec: number };

export function freshness(s: OrgPulseSnapshot, nowMs: number): Freshness {
  if (!s.dataAsOfMs) return { state: "none" };
  const behindSec = Math.max(0, Math.round((nowMs - s.dataAsOfMs) / 1000));
  return nowMs - s.dataAsOfMs > DELAYED_AFTER_MS ? { state: "delayed", behindSec } : { state: "live", behindSec };
}

// Tokens per minute over the last minute of receipts. Null until the window is
// at least five seconds wide, so a fresh page never shows a rate it cannot know.
export type RateSample = { t: number; tokens: number };

export function tokenRatePerMin(samples: readonly RateSample[], nowMs: number): number | null {
  if (samples.length < 2) return null;
  const last = samples[samples.length - 1];
  const old = samples.find((s) => s.t >= nowMs - 60_000) ?? samples[0];
  const spanMin = (last.t - old.t) / 60_000;
  if (spanMin < 5 / 60) return null;
  return Math.max(0, (last.tokens - old.tokens) / spanMin);
}

import { formatUsd } from "./format.ts";

// Changes between two snapshots, as short sentences. A counter that went down
// (the UTC day rolled over) reads as no change, never as a negative event.
export function changesBetween(prev: OrgPulseSnapshot, cur: OrgPulseSnapshot): string[] {
  const out: string[] = [];
  const d = (a: number, b: number) => (b - a > 0 ? b - a : 0);
  for (const [core, lane] of Object.entries(cur.today.repliesByCore)) {
    const n = d(prev.today.repliesByCore[core]?.replies ?? 0, lane.replies);
    if (n) out.push(`+${plural(n, "agent reply", "agent replies")} · ${core}`);
  }
  const m = d(prev.today.messages, cur.today.messages);
  if (m) out.push(`+${plural(m, "message", "messages")}`);
  const dl = d(deliverablesToday(prev), deliverablesToday(cur));
  if (dl) out.push(`+${plural(dl, "deliverable", "deliverables")}`);
  const aw = d(prev.today.appWrites, cur.today.appWrites);
  if (aw) out.push(`+${plural(aw, "app write", "app writes")}`);
  const f = d(prev.today.jobsFinished, cur.today.jobsFinished);
  if (f) out.push(`${plural(f, "job", "jobs")} finished`);
  if (prev.live.agentsWorking !== cur.live.agentsWorking) {
    out.push(`agents working ${prev.live.agentsWorking} → ${cur.live.agentsWorking}`);
  }
  return out;
}

// A plain sentence built from the numbers. No model writes it.
export function summaryText(s: OrgPulseSnapshot): string {
  const t = s.today;
  const cov = t.coverage == null ? "no replies to report on" : `${pct(t.coverage)} with usage reported`;
  const week =
    s.week.activePeopleSuppressed || s.week.activePeople == null
      ? "fewer than 5 people were active this week"
      : `${fmt(s.week.activePeople)} people were active this week`;
  return (
    `${plural(t.replies, "agent reply", "agent replies")}, ${cov}. ` +
    `${fmt(t.tokensIn + t.tokensOut)} tokens, about ${formatUsd(t.costUsd)} reported. ` +
    `${plural(t.jobsClaimed, "job", "jobs")} claimed, ${fmt(t.jobsFinished)} finished. ` +
    `${plural(t.messages, "message", "messages")}, ${plural(deliverablesToday(s), "deliverable", "deliverables")} and ${plural(t.appWrites, "app write", "app writes")}. ` +
    `${week}.`
  );
}

export function fmt(n: number): string {
  return Math.round(n).toLocaleString("en-US");
}

export function pct(v: number): string {
  return (v * 100).toFixed(1) + "%";
}

export function plural(n: number, one: string, many: string): string {
  return `${fmt(n)} ${n === 1 ? one : many}`;
}
